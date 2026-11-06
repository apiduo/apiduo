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
package main

import (
	"flag"
	"fmt"
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

// hunkPattern 取 unified diff 的「新文件起始行号」。
var hunkPattern = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

const (
	baseFile     = ".apiduo/sync-base"
	baselineFile = ".apiduo/marker-baseline.txt"

	headerFormat = "%-6s %-6s %-6s %s\n"
	reportFormat = "%-6d %-6d %-6d %s\n"
)

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
var skipName = map[string]struct{}{
	"routeTree.gen.ts": {},
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
	flag.Parse()

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
		fmt.Printf("已重写 %s（%d 个文件，共 %d 行）\n", baselineFile, len(unmarked), sum(unmarked))
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

	fmt.Printf("\n标记块覆盖检查通过（遗留欠账 %d 个文件 / %d 行）\n", len(unmarked), sum(unmarked))
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
func scan(base string, verbose bool) ([]fileReport, error) {
	changed, err := changedFiles(base)
	if err != nil {
		return nil, fmt.Errorf("读取改动文件失败: %w", err)
	}

	var reports []fileReport
	for _, file := range changed {
		// 基线提交里不存在 = 我方新增的文件，上游永远不会碰，没有冲突面。
		if !existsAt(base, file) {
			continue
		}
		if skipByFormat(file) {
			continue
		}
		added, deletions, err := diffCounts(base, file)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 的差异失败: %w", file, err)
		}
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
			deletions: deletions,
		})
	}
	return reports, nil
}

func changedFiles(base string) ([]string, error) {
	out, err := gitOutput("diff", "--name-only", base, "HEAD")
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

// existsAt 判断文件在基线提交里是否已存在，即它是不是上游的文件。
func existsAt(base, file string) bool {
	_, err := gitOutput("cat-file", "-e", base+":"+file)
	return err == nil
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

// diffCounts 返回新增行在新文件里的行号，以及删除的行数。二进制文件返回空切片。
func diffCounts(base, file string) ([]int, int, error) {
	out, err := gitOutput("diff", "-U0", base, "HEAD", "--", file)
	if err != nil {
		return nil, 0, err
	}
	var added []int
	deletions := 0
	next := 0
	for _, line := range strings.Split(out, "\n") {
		if match := hunkPattern.FindStringSubmatch(line); match != nil {
			next, _ = strconv.Atoi(match[1])
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			added = append(added, next)
			next++
		case strings.HasPrefix(line, "-"):
			deletions++
		case strings.HasPrefix(line, " "):
			next++
		}
	}
	return added, deletions, nil
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
