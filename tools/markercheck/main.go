// Command markercheck 校验「自有代码是否都写在标记块内」。
//
// 背景：fork 里的每一行自有代码都是未来合并上游时的冲突面。约定是把它包进
//
//	/* APIDUO:<feature>:BEGIN */  ...  /* APIDUO:<feature>:END */
//
// 但约定靠人记必然漂——一个属性、一行 import 顺手加上去，三个月后就没人能说清
// 哪些行是自己的。这个命令把约定变成机器可校验的判据。
//
// 两个概念要分清：
//
//	基线 .apiduo/sync-base  上次同步到的「上游提交」。它决定哪些文件算上游的文件，
//	                        以及从哪个点开始算自有代码。
//	遗留 .apiduo/marker-baseline.txt
//	                        同步点之前就已存在、尚未标记的行数，按文件记账。
//	                        这是历史欠账，只允许下调，不允许上涨。
//
// 判据：上游已有的文件里，自基线以来新增的非空行必须落在标记块内；违规数不得超过
// 遗留基线。上游没有的文件不参与检查——它们永远不会冲突。
//
// 用法：
//
//	go run ./tools/markercheck                 # 检查，有新增违规退出码 1
//	go run ./tools/markercheck -v              # 附带每个文件删除上游行的数量
//	go run ./tools/markercheck -update         # 按当前实际情况重写遗留基线
//	go run ./tools/markercheck -base <sha>     # 临时指定基线（排查用）
//	go run ./tools/markercheck -cost           # 按文件打印合并面并与预算比对
//	go run ./tools/markercheck -cost -update   # 收紧合并面预算
//	go run ./tools/markercheck -heat           # 合并面按上游热度加权（隐含 -cost）
//
// 合并面预算 .apiduo/merge-budget.txt 记总量（上游文件数 / 标记块数 / 新增行 /
// 删除上游行），只允许下调：功能收尾时跑 -cost -update 把数字压到新的实际值，
// 想让它上涨就必须显式改一次预算，这比靠自觉可靠。
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// markerPattern 同时匹配 Go 与 TS/TSX 两种注释外壳，只取特征名与方向。
var markerPattern = regexp.MustCompile(`APIDUO:([a-z0-9-]+):(BEGIN|END)`)

// hunkPattern 取 unified diff 两侧的起始行号：旧文件（base）与新文件（工作区）。
// 删除行要按旧行号定位到 base 版本才能 blame 作者，所以两边都要。
var hunkPattern = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

const (
	baseFile     = ".apiduo/sync-base"
	baselineFile = ".apiduo/marker-baseline.txt"

	headerFormat = "%-6s %-6s %-6s %s\n"
	reportFormat = "%-6d %-6d %-6d %s\n"
)

const budgetFile = ".apiduo/merge-budget.txt"

// budgetHeader 记录合并面的三个总量。单个文件允许波动，总量只允许下降：
// 一个功能多占 20 块是设计失误，一百个功能各多占 2 块才是真正把 rebase 拖死的
// 原因，而后者在逐文件视图里看不出来。
const budgetHeader = `# 上游合并面预算：上游文件里我方插入的行、标记块、以及删除的上游行。
# 只允许下调。改完功能跑 go run ./tools/markercheck -cost 看明细，
# 确认下降后跑 go run ./tools/markercheck -cost -update 收紧。
# 格式：<key>	<value>
`

const baselineHeader = `# 历史遗留：同步点之前新增、尚未标记的非空行数。
# 只允许下调。新增违规会让 CI 变红，用 go run ./tools/markercheck -update 收紧。
# 格式：<行数>	<文件>
`

// skipExt 是放不下注释的格式。JSON 没有注释语法，锁文件由工具生成且必然整块变动，
// 两者都不在标记约定能覆盖的范围内。
var skipExt = map[string]struct{}{
	".json": {},
	".lock": {},
	".sum":  {},
	".svg":  {},
}

// skipName 是生成文件：内容由生成器决定，手写标记会被下次生成覆盖。
//
// go.mod 属于同一类，只是它没有扩展名。依赖行是 require 列表的一部分，
// `go mod tidy` 会按模块名重排并合并同块的行——把新依赖包进 /* */ 或加行尾
// // APIDUO 注释，下一次 tidy 就会打散，标记变成噪音。上游合并时的依赖冲突
// 是版本号的语义冲突，靠 tidy + 编译解决，不是行级标记能表达的。
var skipName = map[string]struct{}{
	"routeTree.gen.ts": {},
	"go.mod":           {},
}

type fileReport struct {
	file      string
	added     int
	covered   int
	unmarked  int
	deletions int
}

func main() {
	baseFlag := flag.String("base", "", "覆盖基线提交（默认读 "+baseFile+"）")
	update := flag.Bool("update", false, "按当前实际情况重写 "+baselineFile)
	verbose := flag.Bool("v", false, "打印删除上游行的明细")
	cost := flag.Bool("cost", false, "按文件打印合并面（块数/新增行/删除行）并与预算比对")
	heat := flag.Bool("heat", false, "按上游改动频率给合并面加权（隐含 -cost，只报数不判定）")
	flag.Parse()
	if *heat {
		*cost = true
	}

	root, err := repositoryRoot()
	if err != nil {
		fail("定位仓库根目录失败: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		fail("进入仓库根目录失败: %v", err)
	}

	base, err := resolveBase(*baseFlag)
	if err != nil {
		fail("%v", err)
	}
	subject, _ := gitOutput("log", "-1", "--format=%s", base)
	fmt.Printf("基线 %s  %s\n", shorten(base), subject)

	reports, err := scan(base, *verbose)
	if err != nil {
		fail("%v", err)
	}

	if *cost {
		if err := reportCost(reports, *update, *heat); err != nil {
			fail("%v", err)
		}
		return
	}

	unmarked := make(map[string]int, len(reports))
	for _, report := range reports {
		if report.unmarked > 0 {
			unmarked[report.file] = report.unmarked
		}
	}

	if *update {
		if err := writeBaseline(unmarked); err != nil {
			fail("写入遗留基线失败: %v", err)
		}
		if err := writeBudget(budgetTotals(reports)); err != nil {
			fail("写入合并面预算失败: %v", err)
		}
		fmt.Printf("已重写 %s（%d 个文件，共 %d 行）与 %s\n", baselineFile, len(unmarked), sum(unmarked), budgetFile)
		return
	}

	baseline, err := readBaseline()
	if err != nil {
		fail("%v", err)
	}

	printReports(reports, baseline, *verbose)

	var grew, shrank []string
	for file, count := range unmarked {
		switch allowed := baseline[file]; {
		case count > allowed:
			grew = append(grew, fmt.Sprintf("%s  基线 %d -> 实际 %d（多出 %d 行）", file, allowed, count, count-allowed))
		case count < allowed:
			shrank = append(shrank, fmt.Sprintf("%s  %d -> %d", file, allowed, count))
		}
	}
	for file, allowed := range baseline {
		if _, still := unmarked[file]; !still {
			shrank = append(shrank, fmt.Sprintf("%s  %d -> 0", file, allowed))
		}
	}
	sort.Strings(grew)
	sort.Strings(shrank)

	if len(shrank) > 0 {
		fmt.Printf("\n低于遗留基线，可以收紧（%d 个文件）：\n", len(shrank))
		for _, line := range shrank {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println("  确认无误后跑 go run ./tools/markercheck -update")
	}

	if len(grew) > 0 {
		fmt.Printf("\n新增未标记代码（%d 个文件）：\n", len(grew))
		for _, line := range grew {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println("\n把这些行包进 /* APIDUO:<feature>:BEGIN */ ... /* APIDUO:<feature>:END */。")
		fmt.Println("上游已有的文件里，新增行不在标记块内，合并时就无法区分它属于谁。")
		os.Exit(1)
	}

	if grew := budgetGrowth(reports); len(grew) > 0 {
		fmt.Printf("\n合并面超过预算（%d 项）：\n", len(grew))
		for _, line := range grew {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println("\n新增挂接点要用在上游文件里不可替代的位置，能挪进新文件的就挪。")
		os.Exit(1)
	}

	fmt.Printf("\n标记块覆盖检查通过（遗留欠账 %d 个文件 / %d 行）\n", len(unmarked), sum(unmarked))
}

// heatWindow 是热度统计窗口。180 天约两个季度：短到能反映当前活跃度，
// 长到不会被单次重构事件主导。
const heatWindow = "180 days ago"

// 分档阈值按本仓实测分布定 —— 当前最热的上游文件是 9 次/180 天。
// 若按教科书式的「≤3 为冷」分档，H2/H3 会永远是空集，"热文件"这个指标
// 就变成了一个恒为 0 的数字，看了等于没看。
var heatTiers = []struct {
	name   string
	upTo   int // 含上限
	weight int
}{
	{"H0", 2, 1},
	{"H1", 5, 2},
	{"H2", 10, 4},
	{"H3", math.MaxInt, 8},
}

// heatTierOf 返回热度所属档位与其风险权重。权重是二倍的等比数列：
// 上游每多改一个数量级，落在该文件里的同一块代码被语义冲突破坏的概率
// 大致也翻倍，而不是线性增长。
func heatTierOf(heat int) (string, int) {
	for _, tier := range heatTiers {
		if heat <= tier.upTo {
			return tier.name, tier.weight
		}
	}
	return "H3", heatTiers[len(heatTiers)-1].weight
}

func isHot(tier string) bool { return tier == "H2" || tier == "H3" }

// upstreamHeat 统计每个文件在上游近 heatWindow 内被多少个提交改过。
//
// 一次 git log 拿全量，而不是逐文件调用：146 个文件逐个 log 要十几次进程启动，
// 而 --name-only 的输出本身就是「提交 × 文件」的展开，计数即可。
// 同一提交里同一文件只出现一次，所以计数等于「改过它的提交数」。
//
// upstream/main 不存在时返回 nil：浅克隆里可能没有这个 ref，热度是观测项，
// 不该让检查因此失败。
func upstreamHeat() map[string]int {
	out, err := gitOutput("log", "--since="+heatWindow, "--name-only", "--format=", "upstream/main")
	if err != nil {
		return nil
	}
	heat := make(map[string]int)
	for line := range strings.SplitSeq(out, "\n") {
		if file := strings.TrimSpace(line); file != "" {
			heat[file]++
		}
	}
	return heat
}

// costRow 是一个上游文件的合并面：块越多、删得越多，未来 rebase 越难。
type costRow struct {
	file      string
	blocks    int
	added     int
	deletions int
	heat      int
	tier      string
	weight    int
	weighted  int
}

func countBlocks(path string) int {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, match := range markerPattern.FindAllSubmatch(content, -1) {
		if string(match[2]) == "BEGIN" {
			count++
		}
	}
	return count
}

func costRows(reports []fileReport, heat map[string]int) []costRow {
	rows := make([]costRow, 0, len(reports))
	for _, report := range reports {
		row := costRow{
			file:      report.file,
			blocks:    countBlocks(report.file),
			added:     report.added,
			deletions: report.deletions,
		}
		if heat != nil {
			row.heat = heat[report.file]
			row.tier, row.weight = heatTierOf(row.heat)
			row.weighted = row.blocks * row.weight
		}
		rows = append(rows, row)
	}
	// 有热度时分档优先：块数相同但一个在冷文件、一个在热文件，代价差一个量级。
	if heat != nil {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].weighted != rows[j].weighted {
				return rows[i].weighted > rows[j].weighted
			}
			return rows[i].file < rows[j].file
		})
		return rows
	}
	// 删除上游行排最前：新增行通常还能和上游的改动共存，删掉的行必然冲突。
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].deletions != rows[j].deletions {
			return rows[i].deletions > rows[j].deletions
		}
		if rows[i].blocks != rows[j].blocks {
			return rows[i].blocks > rows[j].blocks
		}
		return rows[i].file < rows[j].file
	})
	return rows
}

var budgetKeys = []string{"files", "blocks", "added", "deleted"}

func budgetTotals(reports []fileReport) map[string]int {
	totals := map[string]int{"files": len(reports)}
	for _, report := range reports {
		totals["blocks"] += countBlocks(report.file)
		totals["added"] += report.added
		totals["deleted"] += report.deletions
	}
	return totals
}

func readBudget() (map[string]int, error) {
	budget := make(map[string]int, len(budgetKeys))
	content, err := os.ReadFile(budgetFile)
	if err != nil {
		if os.IsNotExist(err) {
			return budget, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, convErr := strconv.Atoi(fields[1])
		if convErr != nil {
			continue
		}
		budget[fields[0]] = value
	}
	return budget, nil
}

func writeBudget(totals map[string]int) error {
	var out strings.Builder
	out.WriteString(budgetHeader)
	for _, key := range budgetKeys {
		fmt.Fprintf(&out, "%s\t%d\n", key, totals[key])
	}
	return os.WriteFile(budgetFile, []byte(out.String()), 0o644)
}

// budgetGrowth 只比总量，不比单文件：单个功能多占几块是设计取舍，一百个功能
// 各多占两块才是把 rebase 拖死的原因，而后者在逐文件视图里看不见。
func budgetGrowth(reports []fileReport) []string {
	budget, err := readBudget()
	if err != nil || len(budget) == 0 {
		return nil
	}
	totals := budgetTotals(reports)
	var grew []string
	for _, key := range budgetKeys {
		allowed, ok := budget[key]
		if ok && totals[key] > allowed {
			grew = append(grew, fmt.Sprintf("%s  %d -> %d（多出 %d）", key, allowed, totals[key], totals[key]-allowed))
		}
	}
	return grew
}

func reportCost(reports []fileReport, update, withHeat bool) error {
	var heat map[string]int
	if withHeat {
		if heat = upstreamHeat(); heat == nil {
			fmt.Println("upstream/main 不可用，跳过热度分档")
		}
	}
	rows := costRows(reports, heat)
	if heat != nil {
		fmt.Printf("%-7s %-6s %-6s %-5s %-6s %s\n", "heat", "blocks", "added", "del", "w", "file")
		for _, row := range rows {
			fmt.Printf("%-7s %-6d %-6d %-5d %-6d %s\n",
				fmt.Sprintf("%s/%d", row.tier, row.heat), row.blocks, row.added, row.deletions, row.weighted, row.file)
		}
	} else {
		fmt.Printf("%-6s %-6s %-6s %s\n", "blocks", "added", "del", "file")
		for _, row := range rows {
			fmt.Printf("%-6d %-6d %-6d %s\n", row.blocks, row.added, row.deletions, row.file)
		}
	}

	totals := budgetTotals(reports)
	fmt.Printf("\n合计：上游文件 %d 个，标记块 %d 个，新增 %d 行，删除上游 %d 行\n",
		totals["files"], totals["blocks"], totals["added"], totals["deleted"])
	if heat != nil {
		reportHeat(rows)
	}

	if update {
		if err := writeBudget(totals); err != nil {
			return err
		}
		fmt.Printf("已写入 %s\n", budgetFile)
		return nil
	}

	budget, err := readBudget()
	if err != nil {
		return err
	}
	if len(budget) == 0 {
		fmt.Println("尚无预算基线，确认数字后跑 go run ./tools/markercheck -cost -update 建立。")
		return nil
	}
	for _, key := range budgetKeys {
		allowed, ok := budget[key]
		if !ok {
			continue
		}
		switch delta := totals[key] - allowed; {
		case delta > 0:
			fmt.Printf("  %s 超预算 +%d（%d -> %d）\n", key, delta, allowed, totals[key])
		case delta < 0:
			fmt.Printf("  %s 可收紧 %d（%d -> %d）\n", key, -delta, allowed, totals[key])
		}
	}
	return nil
}

// reportHeat 只报数，不参与任何判定。
//
// 热度的分母（上游提交数）每天都在变，把它写进预算会让检查随上游活动随机失败 ——
// 那不是信号，是噪声。它的用途是排优先级，不是设闸门。
func reportHeat(rows []costRow) {
	var hotFiles, hotBlocks, blocks, weighted int
	for _, row := range rows {
		blocks += row.blocks
		weighted += row.weighted
		if isHot(row.tier) {
			hotFiles++
			hotBlocks += row.blocks
		}
	}
	share := 0
	if blocks > 0 {
		share = hotBlocks * 100 / blocks
	}
	fmt.Printf("热文件（H2+H3）：%d 个 / %d 块，占合并面 %d%%\n", hotFiles, hotBlocks, share)
	fmt.Printf("加权合并面：%d（H0=1 H1=2 H2=4 H3=8）\n", weighted)
}

func repositoryRoot() (string, error) {
	out, err := gitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func resolveBase(override string) (string, error) {
	base := override
	if base == "" {
		raw, err := os.ReadFile(baseFile)
		if err != nil {
			return "", fmt.Errorf("读不到基线文件 %s（内容应为上次同步到的上游提交 SHA）", baseFile)
		}
		base = strings.TrimSpace(string(raw))
	}
	if base == "" {
		return "", fmt.Errorf("%s 是空的，写入上次同步到的上游提交 SHA", baseFile)
	}
	if _, err := gitOutput("rev-parse", "--verify", base+"^{commit}"); err != nil {
		return "", fmt.Errorf("基线 %s 不在本仓库历史里，检查是否浅克隆", base)
	}
	return base, nil
}

// scan 逐个检查上游已有的、且自基线以来被改过的文件。
//
// 三处批量化是必须的：改动文件可能有 800 个，逐个 git 调用（cat-file 判存在、
// diff 取差异）合计 20 秒以上，而检查器一旦跑得久就会被跳过，跳过的闸门等于没有。
// 现在用一次 ls-tree 判存在、一次 diff 取全部差异，实测回到 3 秒级。
func scan(base string, verbose bool) ([]fileReport, error) {
	changed, err := changedFiles(base)
	if err != nil {
		return nil, fmt.Errorf("读取改动文件失败: %w", err)
	}
	baseSet, err := baseFiles(base)
	if err != nil {
		return nil, fmt.Errorf("读取基线文件清单失败: %w", err)
	}

	// 基线提交里不存在 = 我方新增的文件，上游永远不会碰，没有冲突面。
	targets := make([]string, 0, len(changed))
	for _, file := range changed {
		if _, ok := baseSet[file]; !ok {
			continue
		}
		if skipByFormat(file) {
			continue
		}
		targets = append(targets, file)
	}

	diffs, err := diffByFile(base, targets)
	if err != nil {
		return nil, fmt.Errorf("读取差异失败: %w", err)
	}

	var reports []fileReport
	for _, file := range targets {
		added, deleted := countDiff(diffs[file])
		if len(added) == 0 {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			// 在这个范围内被删掉的上游文件。删除本身要人工确认，但不是标记块
			// 能表达的事，交给评审。
			continue
		}
		inside, err := markedLines(content)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		outside := outsideLines(content, added, inside)
		reports = append(reports, fileReport{
			file:      file,
			added:     len(added),
			covered:   len(added) - len(outside),
			unmarked:  len(outside),
			deletions: upstreamDeletions(base, file, deleted),
		})
	}
	return reports, nil
}

// baseFiles 返回基线提交里的全部路径，用于判断某个文件是不是上游本来就有的。
func baseFiles(base string) (map[string]struct{}, error) {
	out, err := gitOutput("ls-tree", "-r", "--name-only", base)
	if err != nil {
		return nil, err
	}
	files := make(map[string]struct{}, 1<<13)
	for file := range strings.SplitSeq(out, "\n") {
		if file = strings.TrimSpace(file); file != "" {
			files[file] = struct{}{}
		}
	}
	return files, nil
}

// diffByFile 一次取回全部文件的差异，按工作区路径索引。
func diffByFile(base string, files []string) (map[string]string, error) {
	if len(files) == 0 {
		return map[string]string{}, nil
	}
	args := make([]string, 0, len(files)+4)
	args = append(args, "diff", "-U0", base, "--")
	args = append(args, files...)
	out, err := gitOutput(args...)
	if err != nil {
		return nil, err
	}

	diffs := make(map[string]string, len(files))
	var current string
	var body strings.Builder
	flush := func() {
		if current != "" {
			diffs[current] = body.String()
		}
		body.Reset()
	}
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
			flush()
			// "a/<old> b/<new>"：取右侧路径，重命名后要的是工作区这一侧。
			_, after, found := strings.Cut(rest, " b/")
			if found {
				current = after
			} else {
				current = ""
			}
			continue
		}
		if current != "" {
			body.WriteString(line)
			body.WriteString("\n")
		}
	}
	flush()
	return diffs, nil
}

func changedFiles(base string) ([]string, error) {
	// 不加 HEAD：比较的是基线提交与工作区，这样提交前的改动也会计入合并面。
	// 只比 base..HEAD 会让「只在工作区碰过的上游文件」凭空消失，而那恰恰是
	// 功能开发中最需要盯住的时刻。
	out, err := gitOutput("diff", "--name-only", base)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			files = append(files, trimmed)
		}
	}
	return files, nil
}

func skipByFormat(file string) bool {
	if _, ok := skipName[filepath.Base(file)]; ok {
		return true
	}
	if strings.HasSuffix(filepath.Base(file), ".gen.ts") {
		return true
	}
	_, ok := skipExt[filepath.Ext(file)]
	return ok
}

// countDiff 从一段 unified diff 里取出新增行（工作区行号）与被删行（基线行号）。
// 二进制文件与纯删除都没有新增行，返回空切片。
//
// 行号必须按「基线 vs 工作区」对齐：markedLines 读的是工作区文件，两边错位就会把
// 已标记的行误判成违规。早先比较的是 HEAD，本地只要有一点未提交改动就会整体偏移。
func countDiff(diff string) (added, deleted []int) {
	next, oldNext := 0, 0
	for line := range strings.SplitSeq(diff, "\n") {
		if match := hunkPattern.FindStringSubmatch(line); match != nil {
			oldNext, _ = strconv.Atoi(match[1])
			next, _ = strconv.Atoi(match[2])
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added = append(added, next)
			next++
		case strings.HasPrefix(line, "-"):
			deleted = append(deleted, oldNext)
			oldNext++
		case strings.HasPrefix(line, " "):
			next++
			oldNext++
		}
	}
	return added, deleted
}

// upstreamCommitSet 是 upstream/main 的全部提交，用于判断某一行是不是上游写的。
//
// 一次 rev-list 拿全量，比逐行 merge-base 快两个数量级；几万条字符串几 MB，
// 只在进程内活一次。返回 nil 表示上游引用不可用（浅克隆 / 没配 remote），
// 此时无法判断作者，调用方退回「全部算上游」的旧口径。
var upstreamCommitSet map[string]struct{}

func loadUpstreamCommits() map[string]struct{} {
	if upstreamCommitSet != nil {
		return upstreamCommitSet
	}
	out, err := gitOutput("rev-list", "upstream/main")
	if err != nil {
		return nil
	}
	set := make(map[string]struct{}, 1<<14)
	for sha := range strings.SplitSeq(out, "\n") {
		if sha = strings.TrimSpace(sha); sha != "" {
			set[sha] = struct{}{}
		}
	}
	upstreamCommitSet = set
	return set
}

// contiguousRanges 把有序行号合并成连续区间。blame 的 -L 只接受区间，
// 逐行调用会让进程启动开销压过计算本身。
func contiguousRanges(lines []int) [][2]int {
	var ranges [][2]int
	for _, n := range lines {
		if len(ranges) > 0 {
			last := &ranges[len(ranges)-1]
			if last[1]+1 == n {
				last[1] = n
				continue
			}
		}
		ranges = append(ranges, [2]int{n, n})
	}
	return ranges
}

// upstreamDeletions 统计被删的行里有多少行真的是上游写的。
//
// 为什么不直接数 diff 的「-」行：等价重构（把标记块里的代码搬进新文件）会让这些行
// 从原文件消失，但上游从未写过它们，删掉不增加任何冲突面。只有覆盖掉上游那行，
// 才是一次必然冲突 —— 那两个数字在预算里差一个量级，混在一起就没法判断风险。
//
// 判断不了上游引用时返回总数：宁可高估，也不能把真实冲突藏起来。
func upstreamDeletions(base, file string, deleted []int) int {
	if len(deleted) == 0 {
		return 0
	}
	commits := loadUpstreamCommits()
	if commits == nil {
		return len(deleted)
	}
	total := 0
	for _, span := range contiguousRanges(deleted) {
		// --abbrev=40：blame 默认输出 10 位缩写，而 rev-list 给的是完整 SHA，
		// 两边长度不一致会让判断恒为「不是上游」—— 这个 bug 曾让 deleted 静默归零。
		out, err := gitOutput("blame", "-s", "--abbrev=40", "-L", fmt.Sprintf("%d,%d", span[0], span[1]), base, "--", file)
		if err != nil {
			return len(deleted)
		}
		for line := range strings.SplitSeq(out, "\n") {
			sha, _, _ := strings.Cut(strings.TrimSpace(line), " ")
			// blame 会给「引入边界」的提交打 ^ 前缀，判断所属前先摘掉。
			if _, ok := commits[strings.TrimPrefix(sha, "^")]; ok {
				total++
			}
		}
	}
	return total
}

// markedLines 返回落在标记块内的行号（含标记行本身）。用栈校验配对——写错一个
// END 是最容易发生、也最难在合并时察觉的失误。
func markedLines(content []byte) (map[int]struct{}, error) {
	inside := make(map[int]struct{})
	var stack []string
	for index, line := range strings.Split(string(content), "\n") {
		number := index + 1
		if match := markerPattern.FindStringSubmatch(line); match != nil {
			inside[number] = struct{}{}
			feature, kind := match[1], match[2]
			if kind == "BEGIN" {
				stack = append(stack, feature)
				continue
			}
			if len(stack) == 0 {
				return nil, fmt.Errorf("第 %d 行的 END 没有对应的 BEGIN", number)
			}
			if top := stack[len(stack)-1]; top != feature {
				return nil, fmt.Errorf("第 %d 行的 END:%s 配到了 BEGIN:%s", number, feature, top)
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if len(stack) > 0 {
			inside[number] = struct{}{}
		}
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("标记块未闭合: %s", stack[len(stack)-1])
	}
	return inside, nil
}

// outsideLines 只统计非空行：块与块之间的空行是排版需要，不构成实质冲突面。
func outsideLines(content []byte, added []int, inside map[int]struct{}) []int {
	lines := strings.Split(string(content), "\n")
	var outside []int
	for _, number := range added {
		if _, ok := inside[number]; ok {
			continue
		}
		if number-1 >= len(lines) {
			continue
		}
		if strings.TrimSpace(lines[number-1]) == "" {
			continue
		}
		outside = append(outside, number)
	}
	return outside
}

func readBaseline() (map[string]int, error) {
	raw, err := os.ReadFile(baselineFile)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", baselineFile, err)
	}
	baseline := make(map[string]int)
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, "\t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%s 格式错误（应为 <行数>\\t<文件>）: %q", baselineFile, trimmed)
		}
		count, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("%s 的行数不是数字: %q", baselineFile, trimmed)
		}
		baseline[strings.TrimSpace(parts[1])] = count
	}
	return baseline, nil
}

func writeBaseline(unmarked map[string]int) error {
	files := make([]string, 0, len(unmarked))
	for file := range unmarked {
		files = append(files, file)
	}
	sort.Strings(files)

	var builder strings.Builder
	builder.WriteString(baselineHeader)
	for _, file := range files {
		fmt.Fprintf(&builder, "%d\t%s\n", unmarked[file], file)
	}
	if err := os.MkdirAll(filepath.Dir(baselineFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(baselineFile, []byte(builder.String()), 0o644)
}

func printReports(reports []fileReport, baseline map[string]int, verbose bool) {
	if len(reports) == 0 {
		fmt.Println("自基线以来没有改动上游已有的文件")
		return
	}
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].unmarked != reports[j].unmarked {
			return reports[i].unmarked > reports[j].unmarked
		}
		return reports[i].file < reports[j].file
	})
	fmt.Printf("\n"+headerFormat, "新增", "已标记", "未标记", "文件（按未标记降序）")
	for _, report := range reports {
		marker := ""
		if allowed, ok := baseline[report.file]; ok && report.unmarked > allowed {
			marker = fmt.Sprintf("  ← 基线 %d", allowed)
		}
		fmt.Printf(reportFormat, report.added, report.covered, report.unmarked, report.file+marker)
		if verbose && report.deletions > 0 {
			fmt.Printf("       └ 删除上游行 %d\n", report.deletions)
		}
	}
}

func sum(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func gitOutput(args ...string) (string, error) {
	command := exec.Command("git", args...)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func shorten(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "markercheck: "+format+"\n", args...)
	os.Exit(2)
}
