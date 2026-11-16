package model

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// OpsSeriesPoint 是服务质量时间线上的一个桶。
//
// 三个数放在一起是有意的：错误率、失败数、延迟分位必须同屏对比。
// 只看错误率会漏掉最隐蔽的劣化——一条错误都不报，但整体变慢；
// 只看延迟又会把一个 500 当成功算进去。
type OpsSeriesPoint struct {
	Ts       int64   `json:"ts"`
	Requests int     `json:"requests"`
	Failures int     `json:"failures"`
	FailRate float64 `json:"fail_rate"`
	P50ms    float64 `json:"p50_ms"`
	P95ms    float64 `json:"p95_ms"`
}

// opsSeriesMaxSpan 限制单次查询跨度。24h 约 8 万行日志，
// 取原始行到内存算分位是可接受的；30 天就是 240 万行，会撑爆。
const opsSeriesMaxSpan = int64(8 * 86400)

// ScanOpsSeries 按时间桶聚合请求量、失败请求数与延迟分位。
//
// 分位在 Go 里算而不是 SQL：SQLite 没有 percentile 函数，
// ClickHouse 有但两个库都要能跑，与其写两套不如取原始 use_time。
// 失败按 request_id 去重——日志是按「渠道尝试」写的，重试会留多行。
func ScanOpsSeries(ctx context.Context, from, to, bucket int64, modelFilter string) ([]OpsSeriesPoint, error) {
	if bucket <= 0 {
		bucket = 600
	}
	if to-from > opsSeriesMaxSpan {
		to = from + opsSeriesMaxSpan
	}
	// 日志库是 ClickHouse 时走原生聚合：让数据库算分位，
	// 只回聚合行。否则把几十万行取回 Go，CPU 全花在排序上。
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return scanOpsSeriesCH(ctx, from, to, bucket, modelFilter)
	}
	conn := LOG_DB
	if conn == nil {
		conn = DB
	}

	type row struct {
		CreatedAt int64
		Type      int
		UseTime   int
		UseTimeMs int
		RequestId string
	}
	var rows []row
	err := conn.WithContext(ctx).Model(&Log{}).
		Select("created_at, type, use_time, use_time_ms, request_id").
		Where("created_at >= ? AND created_at < ? AND type IN ?", from, to, []int{LogTypeConsume, LogTypeError}).
		Where("model_name LIKE ?", likeModel(modelFilter)).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	type acc struct {
		requests int
		failSeen map[string]struct{}
		latency  []int
	}
	buckets := map[int64]*acc{}
	at := func(ts int64) *acc {
		key := ts / bucket * bucket
		a, ok := buckets[key]
		if !ok {
			a = &acc{failSeen: map[string]struct{}{}}
			buckets[key] = a
		}
		return a
	}
	for _, r := range rows {
		a := at(r.CreatedAt)
		if r.Type == LogTypeConsume {
			a.requests++
			if ms := latencyMs(r.UseTimeMs, r.UseTime); ms > 0 {
				a.latency = append(a.latency, ms)
			}
			continue
		}
		// 失败的 request_id 可能为空（极早期日志），空串会互相合并，
		// 用时间戳兜底保证是不同的失败。
		id := r.RequestId
		if id == "" {
			id = "anon:" + itoa(r.CreatedAt)
		}
		a.failSeen[id] = struct{}{}
	}

	out := make([]OpsSeriesPoint, 0, len(buckets))
	for ts, a := range buckets {
		failures := len(a.failSeen)
		total := a.requests + failures
		rate := 0.0
		if total > 0 {
			rate = float64(failures) / float64(total)
		}
		sort.Ints(a.latency)
		out = append(out, OpsSeriesPoint{
			Ts:       ts,
			Requests: a.requests,
			Failures: failures,
			FailRate: rate,
			P50ms:    percentile(a.latency, 0.50),
			P95ms:    percentile(a.latency, 0.95),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ts < out[j].Ts })
	return out, nil
}

// latencyMs 取一条请求的毫秒耗时。
// 新日志直接有毫秒值；旧日志只有秒，按 ×1000 回落——
// 逐条降级而不是整体降级，否则一条老数据会把整窗的分位拉回秒精度。
func latencyMs(useTimeMs, useTimeSeconds int) int {
	if useTimeMs > 0 {
		return useTimeMs
	}
	if useTimeSeconds > 0 {
		return useTimeSeconds * 1000
	}
	return 0
}

// percentile 用 nearest-rank 取分位，输入输出都是毫秒。
func percentile(sorted []int, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return float64(sorted[rank-1])
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// likeModel 把筛选词包成 LIKE 模式。空字符串返回 "%"（匹配全部），
// 这样调用方不必在 SQL 里写条件分支。
//
// 用 %x% 而不是 x%：用户往往只记得模型名的中段（qwen-image-3.0 里的 image），
// 前缀匹配会漏。这个站一天几十条请求，全表扫也无所谓。
func likeModel(filter string) string {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return "%"
	}
	return "%" + filter + "%"
}

// OpsBreakdownRow 是按某一维度聚合后的一行。
type OpsBreakdownRow struct {
	Key      string  `json:"key"`
	Requests int     `json:"requests"`
	Failures int     `json:"failures"`
	FailRate float64 `json:"fail_rate"`
	P50ms    float64 `json:"p50_ms"`
	P95ms    float64 `json:"p95_ms"`
}

// ScanOpsBreakdown 按 model 或 channel 下钻。
//
// 维度只能是白名单枚举：直接把 dim 拼进 GROUP BY 会变成注入点，
// 而且写错时容易静默返回全量，让调用方以为筛选生效了。
func ScanOpsBreakdown(ctx context.Context, from, to int64, dim, modelFilter string) ([]OpsBreakdownRow, error) {
	if to-from > opsSeriesMaxSpan {
		to = from + opsSeriesMaxSpan
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return scanOpsBreakdownCH(ctx, from, to, dim, modelFilter)
	}
	conn := LOG_DB
	if conn == nil {
		conn = DB
	}

	type row struct {
		CreatedAt int64
		Type      int
		UseTime   int
		UseTimeMs int
		RequestId string
		ModelName string
		ChannelId int
		Group     string
	}
	var rows []row
	q := conn.WithContext(ctx).Model(&Log{}).
		Select("created_at, type, use_time, use_time_ms, request_id, model_name, channel_id, `group`").
		Where("created_at >= ? AND created_at < ? AND type IN ?", from, to, []int{LogTypeConsume, LogTypeError}).
		Where("model_name LIKE ?", likeModel(modelFilter))
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	type acc struct {
		requests int
		failSeen map[string]struct{}
		latency  []int
	}
	buckets := map[string]*acc{}
	get := func(k string) *acc {
		a, ok := buckets[k]
		if !ok {
			a = &acc{failSeen: map[string]struct{}{}}
			buckets[k] = a
		}
		return a
	}

	for _, r := range rows {
		var key string
		switch dim {
		case "model":
			key = r.ModelName
			if key == "" {
				key = "(unknown)"
			}
		case "channel":
			key = itoa(int64(r.ChannelId))
		case "group":
			key = r.Group
			if key == "" {
				key = "(default)"
			}
		default:
			return nil, errUnknownDim(dim)
		}
		a := get(key)
		if r.Type == LogTypeConsume {
			a.requests++
			if ms := latencyMs(r.UseTimeMs, r.UseTime); ms > 0 {
				a.latency = append(a.latency, ms)
			}
			continue
		}
		id := r.RequestId
		if id == "" {
			id = "anon:" + itoa(r.CreatedAt)
		}
		a.failSeen[id] = struct{}{}
	}

	out := make([]OpsBreakdownRow, 0, len(buckets))
	for k, a := range buckets {
		failures := len(a.failSeen)
		total := a.requests + failures
		rate := 0.0
		if total > 0 {
			rate = float64(failures) / float64(total)
		}
		sort.Ints(a.latency)
		out = append(out, OpsBreakdownRow{
			Key:      k,
			Requests: a.requests,
			Failures: failures,
			FailRate: rate,
			P50ms:    percentile(a.latency, 0.50),
			P95ms:    percentile(a.latency, 0.95),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func errUnknownDim(dim string) error {
	return &unknownDimError{dim: dim}
}

type unknownDimError struct{ dim string }

func (e *unknownDimError) Error() string { return "unknown breakdown dim: " + e.dim }

// ChangeEvent 记录一次可能影响服务质量的变更。
//
// 排障时最常问的两个问题：什么时候开始坏的、之前改过什么。
// 时间线只回答第一个，这张表回答第二个。
type ChangeEvent struct {
	Id         int    `json:"id" gorm:"primaryKey"`
	Kind       string `json:"kind" gorm:"size:32;index;default:''"`
	Title      string `json:"title" gorm:"size:255;default:''"`
	Detail     string `json:"detail" gorm:"type:text"`
	OccurredAt int64  `json:"occurred_at" gorm:"index;default:0"`
	Actor      string `json:"actor" gorm:"size:64;default:''"`
	CreatedAt  int64  `json:"created_at" gorm:"default:0"`
}

// changeQueue 缓冲变更事件，写库在后台做。
//
// 为什么改成异步：调用点在 DisableChannel 里，那是 relay 的错误分支——
// 也就是「上游刚出问题、正在重试」的时刻。这时候再去抢一个 MySQL 连接，
// 等于在系统最脆弱的时候多压一根稻草。
//
// 丢弃策略：缓冲满时直接丢。这与「丢一条可能解释事故的记录」相权衡，
// 结论是「不能因为它阻塞转发」——记录可以补，请求卡住会连锁失败。
var (
	changeQueue     = make(chan ChangeEvent, 256)
	changeQueueOnce sync.Once
)

func startChangeWriter() {
	changeQueueOnce.Do(func() {
		go func() {
			for ev := range changeQueue {
				if err := DB.Create(&ev).Error; err != nil {
					common.SysError(common.LogText("failed to record change event: %s", err.Error()))
				}
			}
		}()
	})
}

// RecordChange 异步写一条变更事件。永不阻塞调用方。
func RecordChange(ev ChangeEvent) {
	if ev.OccurredAt == 0 {
		ev.OccurredAt = nowUnix()
	}
	if ev.CreatedAt == 0 {
		ev.CreatedAt = ev.OccurredAt
	}
	startChangeWriter()
	select {
	case changeQueue <- ev:
	default:
		// 队列满：丢弃。宁可少一条记录，也不让转发路径等待。
	}
}

// ListChangeEvents 按发生时间正序取窗口内的事件。
func ListChangeEvents(from, to int64, limit int) ([]ChangeEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []ChangeEvent
	q := DB.Model(&ChangeEvent{}).Order("occurred_at ASC").Limit(limit)
	if from > 0 {
		q = q.Where("occurred_at >= ?", from)
	}
	if to > 0 {
		q = q.Where("occurred_at <= ?", to)
	}
	err := q.Find(&out).Error
	return out, err
}

func nowUnix() int64 { return time.Now().Unix() }

// ErrorCluster 是同一根因下的一组报错。
// StatusCodeCount 是簇内出现的具体 HTTP 状态码及次数。
//
// 归一化后 502 与 503 合并成 SERVERERROR，但运维需要知道到底是哪个：
// 502 通常是上游网关挂了，503 是上游过载，处置方式不同。
type StatusCodeCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type ErrorCluster struct {
	Fingerprint string `json:"fingerprint"`
	Sample      string `json:"sample"`
	Requests    int    `json:"requests"`
	Users       int    `json:"users"`
	Channels    int    `json:"channels"`
	FirstSeen   int64  `json:"first_seen"`
	LastSeen    int64  `json:"last_seen"`
	// 具体状态码明细，按次数降序；没有状态码的错误为空
	StatusCodes []StatusCodeCount `json:"status_codes,omitempty"`
}

// normalizeErrorMessage 把一条报错压成可用于分组的模板。
//
// 不按原文分组的原因：日志消息里带 request id、余额数额、上游 IP 与端口，
// 同一个根因会因此散成几百个「只出现一次的簇」，聚类反而把问题藏起来。
// 归一化后保留的是「错误的形状」，那才是根因的特征。
func normalizeErrorMessage(s string) string {
	s = strings.ToLower(s)
	// request id 整段抹掉。只靠「长数字串」规则不够：
	// 真实 id 是「数字 + 随机字母」混合（...d9d642igetZm），
	// 数字被吃掉后仍会残留 igetZm，同一条错误还是分不成一组。
	s = requestIdPattern.ReplaceAllString(s, "(request id: )")
	// IP:port 先归一，否则后面的数字规则会把端口吃掉一半
	s = ipPortPattern.ReplaceAllString(s, "..:")
	// Hex/长数字串（request id、时间戳）
	s = longDigitPattern.ReplaceAllString(s, "")
	// 带货币符号的金额
	s = moneyPattern.ReplaceAllString(s, "$")
	// 「数字+单位」保留单位形状：30s 与 45s 是同一个根因，
	// 但如果直接把数字抹掉会剩一个孤零零的 s，读起来像脏数据。
	s = durationPattern.ReplaceAllString(s, "N${2}")
	// HTTP 状态码归一成类别（4xx/5xx）而不是抹掉或保留原值：
	// 抹掉会让 404 和 502 混成一簇（一个是我们的参数错、一个是上游故障，
	// 要找不同的人）；保留原值又会把 502/503 碎成两簇（同一根因）。
	// 类别既保住可操作性，又不碎片化。
	// 类别名不能含数字：后面还有一道「残余数字」规则，
	// "5xx" 里的 5 会被它吃掉，最后只剩 "xx"（线上就是这样翻车的）。
	// 类别名一个数字都不能带：后面还有一道「残余数字」规则，
	// "5xx" 里的 5、"5ERRORCLASS" 里的 5 都会被它吃掉，只剩 "xx" / "ERRORCLASS"。
	// 线上第一次上线就是这样翻车的——status_code 全变成了空。
	s = httpStatusPattern.ReplaceAllStringFunc(s, func(m string) string {
		cut := strings.LastIndexAny(m, "= ")
		if cut < 0 || len(m)-cut-1 != 3 {
			return m
		}
		var class string
		switch m[cut+1] {
		case '2':
			class = "OKSTATUS"
		case '3':
			class = "REDIRECTSTATUS"
		case '4':
			class = "CLIENTERROR"
		default:
			class = "SERVERERROR"
		}
		return m[:cut+1] + class
	})
	// 剩余所有独立数字（含小数）
	s = numberPattern.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

var (
	requestIdPattern = regexp.MustCompile(`\(request id: [^)]*\)`)
	ipPortPattern    = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3}:\d+`)
	longDigitPattern = regexp.MustCompile(`\b[0-9a-f]{12,}\b`)
	moneyPattern     = regexp.MustCompile(`\$\s*\d+(?:\.\d+)?`)
	durationPattern  = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(ms|s|sec|m|min|h|hour)\b`)
	// 匹配 status_code=502 / http 503 / HTTP 500：前缀进组 1、类别进组 2、
	// 末两位丢掉。替换成 CLIENTERROR / SERVERERROR 这类不含数字的标记。
	httpStatusPattern = regexp.MustCompile(`(status_code\s*=\s*|https?\s+)([1-5])\d{2}\b`)
	numberPattern     = regexp.MustCompile(`\d+(?:\.\d+)?`)
	// 从错误原文里捞具体状态码；与 CH 的 extract 正则保持一致
	statusCodeExtract = regexp.MustCompile(`status_code\s*=\s*([1-5]\d{2})`)
)

// ScanErrorClusters 按归一化消息聚类，按影响请求数倒序。
func ScanErrorClusters(ctx context.Context, from, to int64, limit int, modelFilter string) ([]ErrorCluster, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if to-from > opsSeriesMaxSpan {
		to = from + opsSeriesMaxSpan
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return scanErrorClustersCH(ctx, from, to, limit, modelFilter)
	}
	conn := LOG_DB
	if conn == nil {
		conn = DB
	}

	type row struct {
		CreatedAt int64
		UserId    int
		ChannelId int
		RequestId string
		Content   string
	}
	var rows []row
	err := conn.WithContext(ctx).Model(&Log{}).
		Select("created_at, user_id, channel_id, request_id, content").
		Where("created_at >= ? AND created_at < ? AND type = ?", from, to, LogTypeError).
		Where("model_name LIKE ?", likeModel(modelFilter)).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	type acc struct {
		sample   string
		reqSeen  map[string]struct{}
		users    map[int]struct{}
		channels map[int]struct{}
		codes    map[string]int
		first    int64
		last     int64
	}
	buckets := map[string]*acc{}
	for i, r := range rows {
		fp := normalizeErrorMessage(r.Content)
		if fp == "" {
			fp = "(empty message)"
		}
		a, ok := buckets[fp]
		if !ok {
			a = &acc{
				sample:   r.Content,
				reqSeen:  map[string]struct{}{},
				users:    map[int]struct{}{},
				channels: map[int]struct{}{},
				codes:    map[string]int{},
				first:    r.CreatedAt,
				last:     r.CreatedAt,
			}
			buckets[fp] = a
		}
		id := r.RequestId
		if id == "" {
			id = "anon:" + itoa(r.CreatedAt) + ":" + itoa(int64(i))
		}
		a.reqSeen[id] = struct{}{}
		a.users[r.UserId] = struct{}{}
		a.channels[r.ChannelId] = struct{}{}
		if r.CreatedAt < a.first {
			a.first = r.CreatedAt
		}
		if r.CreatedAt > a.last {
			a.last = r.CreatedAt
		}
		if m := statusCodeExtract.FindStringSubmatch(r.Content); m != nil {
			a.codes[m[1]]++
		}
	}

	out := make([]ErrorCluster, 0, len(buckets))
	for fp, a := range buckets {
		out = append(out, ErrorCluster{
			Fingerprint: fp,
			Sample:      a.sample,
			Requests:    len(a.reqSeen),
			Users:       len(a.users),
			Channels:    len(a.channels),
			FirstSeen:   a.first,
			LastSeen:    a.last,
			StatusCodes: sortedStatusCodeCounts(a.codes),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ChannelMarginRow 是一个渠道在时间窗内的收支。
type ChannelMarginRow struct {
	ChannelId  int     `json:"channel_id"`
	RevenueUSD float64 `json:"revenue_usd"`
	CostUSD    float64 `json:"cost_usd"`
	MarginUSD  float64 `json:"margin_usd"`
	MarginPct  float64 `json:"margin_pct"`
	Requests   int     `json:"requests"`
	// Priced 表示这个渠道是否配了单价。
	// 没有单价时 CostUSD 是 0，不能把它读成「零成本高毛利」。
	Priced bool `json:"priced"`
}

// ScanChannelMargins 按渠道汇总收入、成本与毛利。
//
// 只返回配了单价的渠道 + 有流量的渠道：前者是能算清的，
// 后者是「该去配单价了」的提醒。两者混在一张表里，运营才知道
// 下一步该做什么。
func ScanChannelMargins(ctx context.Context, from, to int64) ([]ChannelMarginRow, error) {
	if to-from > opsSeriesMaxSpan {
		to = from + opsSeriesMaxSpan
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return scanChannelMarginsCH(ctx, from, to)
	}
	conn := LOG_DB
	if conn == nil {
		conn = DB
	}

	type row struct {
		ChannelId        int
		CreatedAt        int64
		PromptTokens     int
		CompletionTokens int
		Quota            int
		RequestId        string
	}
	var rows []row
	err := conn.WithContext(ctx).Model(&Log{}).
		Select("channel_id, created_at, prompt_tokens, completion_tokens, quota, request_id").
		Where("created_at >= ? AND created_at < ? AND type = ?", from, to, LogTypeConsume).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	// 读内存副本，不查 MySQL：这是看板接口，不能拿它占数据库连接。
	costByChannel := CachedChannelCosts()

	type acc struct {
		revenue  float64
		cost     float64
		requests int
		seen     map[string]struct{}
	}
	byChannel := map[int]*acc{}
	for _, r := range rows {
		a, ok := byChannel[r.ChannelId]
		if !ok {
			a = &acc{seen: map[string]struct{}{}}
			byChannel[r.ChannelId] = a
		}
		id := r.RequestId
		if id == "" {
			id = "anon:" + itoa(int64(a.requests)) + ":" + itoa(r.CreatedAt)
		}
		if _, dup := a.seen[id]; !dup {
			a.seen[id] = struct{}{}
			a.requests++
		}
		c := ComputeCost(r.PromptTokens, r.CompletionTokens, r.Quota, costByChannel[r.ChannelId])
		a.revenue += c.RevenueUSD
		a.cost += c.CostUSD
	}

	out := make([]ChannelMarginRow, 0, len(byChannel))
	for ch, a := range byChannel {
		margin := a.revenue - a.cost
		pct := 0.0
		if a.revenue > 0 {
			pct = margin / a.revenue * 100
		}
		out = append(out, ChannelMarginRow{
			ChannelId:  ch,
			RevenueUSD: a.revenue,
			CostUSD:    a.cost,
			MarginUSD:  margin,
			MarginPct:  pct,
			Requests:   a.requests,
			Priced:     costByChannel[ch] != nil,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RevenueUSD != out[j].RevenueUSD {
			return out[i].RevenueUSD > out[j].RevenueUSD
		}
		return out[i].ChannelId < out[j].ChannelId
	})
	return out, nil
}

// scanOpsSeriesCH 把聚合与分位全部下推到 ClickHouse。
//
// 为什么必须分这条路径：原来的实现把窗口内全部原始日志取回 Go 再排序，
// 20 万行时单次 260ms、占几百 MB 临时内存。几个并发就能把 CPU 吃满，
// relay 转发跟着劣化（实测 P95 从 141ms 涨到 496ms）。
// ClickHouse 有 quantile，让它算完只回聚合行，代价从「行数」变成「桶数」。
func scanOpsSeriesCH(ctx context.Context, from, to, bucket int64, modelFilter string) ([]OpsSeriesPoint, error) {
	conn := LOG_DB
	type row struct {
		Bucket int64   `gorm:"column:bucket"`
		Req    int     `gorm:"column:req"`
		Fail   int     `gorm:"column:fail"`
		P50    float64 `gorm:"column:p50"`
		P95    float64 `gorm:"column:p95"`
	}
	var rows []row
	err := conn.WithContext(ctx).Raw(`
		SELECT intDiv(created_at, ?) * ? AS bucket,
		       countIf(type = 2) AS req,
		       uniqExactIf(request_id, type = 5) AS fail,
		       if(countIf(type = 2) = 0, 0, quantile(0.5)(if(type = 2, if(use_time_ms > 0, use_time_ms, use_time * 1000), NULL))) AS p50,
		       if(countIf(type = 2) = 0, 0, quantile(0.95)(if(type = 2, if(use_time_ms > 0, use_time_ms, use_time * 1000), NULL))) AS p95
		FROM logs
		WHERE created_at >= ? AND created_at < ? AND type IN (2, 5)
		  AND model_name LIKE ?
		GROUP BY bucket
		ORDER BY bucket
	`, bucket, bucket, from, to, likeModel(modelFilter)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]OpsSeriesPoint, 0, len(rows))
	for _, r := range rows {
		total := r.Req + r.Fail
		rate := 0.0
		if total > 0 {
			rate = float64(r.Fail) / float64(total)
		}
		out = append(out, OpsSeriesPoint{
			Ts:       r.Bucket,
			Requests: r.Req,
			Failures: r.Fail,
			FailRate: rate,
			P50ms:    r.P50,
			P95ms:    r.P95,
		})
	}
	return out, nil
}

// scanOpsBreakdownCH 按维度下推聚合。
// 与 series 同理：分组后只需回几十行，不必把原始日志搬进 Go。
func scanOpsBreakdownCH(ctx context.Context, from, to int64, dim, modelFilter string) ([]OpsBreakdownRow, error) {
	var groupCol string
	switch dim {
	case "model":
		groupCol = "model_name"
	case "channel":
		groupCol = "toString(channel_id)"
	case "group":
		// group 在 ClickHouse 与 MySQL 里都是保留字，不转义会直接语法错误
		groupCol = "`group`"
	default:
		return nil, errUnknownDim(dim)
	}
	type row struct {
		Key  string  `gorm:"column:k"`
		Req  int     `gorm:"column:req"`
		Fail int     `gorm:"column:fail"`
		P50  float64 `gorm:"column:p50"`
		P95  float64 `gorm:"column:p95"`
	}
	var rows []row
	err := LOG_DB.WithContext(ctx).Raw(`
		SELECT `+groupCol+` AS k,
		       countIf(type = 2) AS req,
		       uniqExactIf(request_id, type = 5) AS fail,
		       if(countIf(type = 2) = 0, 0, quantile(0.5)(if(type = 2, if(use_time_ms > 0, use_time_ms, use_time * 1000), NULL))) AS p50,
		       if(countIf(type = 2) = 0, 0, quantile(0.95)(if(type = 2, if(use_time_ms > 0, use_time_ms, use_time * 1000), NULL))) AS p95
		FROM logs
		WHERE created_at >= ? AND created_at < ? AND type IN (2, 5)
		  AND model_name LIKE ?
		GROUP BY k
	`, from, to, likeModel(modelFilter)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]OpsBreakdownRow, 0, len(rows))
	for _, r := range rows {
		key := r.Key
		if key == "" {
			key = "(unknown)"
		}
		total := r.Req + r.Fail
		rate := 0.0
		if total > 0 {
			rate = float64(r.Fail) / float64(total)
		}
		out = append(out, OpsBreakdownRow{
			Key:      key,
			Requests: r.Req,
			Failures: r.Fail,
			FailRate: rate,
			P50ms:    r.P50,
			P95ms:    r.P95,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// scanChannelMarginsCH 把按渠道的用量汇总下推。
// 成本计算仍在 Go 里做——单价是业务配置，不该进 SQL。
func scanChannelMarginsCH(ctx context.Context, from, to int64) ([]ChannelMarginRow, error) {
	type row struct {
		ChannelId  int   `gorm:"column:ch"`
		Req        int   `gorm:"column:req"`
		Prompt     int64 `gorm:"column:pt"`
		Completion int64 `gorm:"column:ct"`
		Quota      int64 `gorm:"column:q"`
	}
	var rows []row
	err := LOG_DB.WithContext(ctx).Raw(`
		SELECT channel_id AS ch,
		       uniqExactIf(request_id, type = 2) AS req,
		       sumIf(prompt_tokens, type = 2) AS pt,
		       sumIf(completion_tokens, type = 2) AS ct,
		       sumIf(quota, type = 2) AS q
		FROM logs
		WHERE created_at >= ? AND created_at < ? AND type = 2
		GROUP BY ch
	`, from, to).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	// 读内存副本，不查 MySQL：这是看板接口，不能拿它占数据库连接。
	costByChannel := CachedChannelCosts()

	out := make([]ChannelMarginRow, 0, len(rows))
	for _, r := range rows {
		c := ComputeCost(int(r.Prompt), int(r.Completion), int(r.Quota), costByChannel[r.ChannelId])
		pct := 0.0
		if c.RevenueUSD > 0 {
			pct = c.MarginUSD / c.RevenueUSD * 100
		}
		out = append(out, ChannelMarginRow{
			ChannelId:  r.ChannelId,
			RevenueUSD: c.RevenueUSD,
			CostUSD:    c.CostUSD,
			MarginUSD:  c.MarginUSD,
			MarginPct:  pct,
			Requests:   r.Req,
			Priced:     costByChannel[r.ChannelId] != nil,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RevenueUSD != out[j].RevenueUSD {
			return out[i].RevenueUSD > out[j].RevenueUSD
		}
		return out[i].ChannelId < out[j].ChannelId
	})
	return out, nil
}

// chNormalizeExpr 是 normalizeErrorMessage 的 ClickHouse 版本。
//
// 两套实现必须逐条对齐，否则同一份数据在两个日志库上会给出不同的簇——
// 这类不一致不会报错，只会让人看到一个只在换库后才出现的「新错误」。
// 顺序尤其重要：Go 是先 ToLower 再替换，所以插入的 "N" 保持大写；
// 如果 CH 写成「替换后再 lower」，结果会变成小写 "ns"，两边就对不上了。
const chNormalizeExpr = `trimBoth(replaceRegexpAll(
    replaceRegexpAll(
      replaceRegexpAll(
        replaceRegexpAll(
          replaceRegexpAll(
            replaceRegexpAll(
              replaceRegexpAll(
                replaceRegexpAll(
                  replaceRegexpAll(lower(content), '\\(request id: [^)]*\\)', '(request id: )'),
                  '\\d{1,3}(?:\\.\\d{1,3}){3}:\\d+', '..:'),
                '\\b[0-9a-f]{12,}\\b', ''),
              '\\$\\s*\\d+(?:\\.\\d+)?', '$'),
            '(\\d+(?:\\.\\d+)?)\\s*(ms|s|sec|m|min|h|hour)\\b', 'N\\2'),
          '(status_code\\s*=\\s*|https?\\s+)5\\d{2}\\b', '\\1SERVERERROR'),
        '(status_code\\s*=\\s*|https?\\s+)4\\d{2}\\b', '\\1CLIENTERROR'),
      '\\d+(?:\\.\\d+)?', ''),
    '\\s+', ' '))`

// scanErrorClustersCH 把归一化与分组一起下推。
//
// 归一化用了 7 次正则替换，在 Go 里对每一行都做一遍；
// 20 万行时这是秒级开销，而 CH 可以在列上并行算完再分组。
func scanErrorClustersCH(ctx context.Context, from, to int64, limit int, modelFilter string) ([]ErrorCluster, error) {
	type row struct {
		Fingerprint string   `gorm:"column:fingerprint"`
		Sample      string   `gorm:"column:sample"`
		Req         int      `gorm:"column:req"`
		Users       int      `gorm:"column:users"`
		Channels    int      `gorm:"column:channels"`
		FirstSeen   int64    `gorm:"column:first_seen"`
		LastSeen    int64    `gorm:"column:last_seen"`
		Codes       []string `gorm:"column:codes"`
	}
	var rows []row
	// 这里不能用 fmt.Sprintf 内联参数：modelFilter 是用户输入，内联就是注入面。
	// 改成 ? 占位符由驱动转义；chNormalizeExpr 里的 (?:...) 是常量拼接，
	// 不经过占位符解析。
	query := `
		SELECT norm AS fingerprint,
		       any(content) AS sample,
		       uniqExact(if(request_id = '', concat('anon:', toString(created_at)), request_id)) AS req,
		       uniqExact(user_id) AS users,
		       uniqExact(channel_id) AS channels,
		       min(created_at) AS first_seen,
		       max(created_at) AS last_seen,
		       -- 归一化把状态码抹成 SERVERERROR 了，这里从原文把具体码捞出来。
		       -- 同一条记录可能没有状态码（空串），Go 侧过滤后再计数。
		       groupArray(extract(content, 'status_code\\s*=\\s*([1-5]\\d{2})')) AS codes
		FROM (
			SELECT request_id, user_id, channel_id, created_at, content, ` + chNormalizeExpr + ` AS norm
			FROM logs
			WHERE created_at >= ? AND created_at < ? AND type = 5
			  AND model_name LIKE ?
		)
		GROUP BY norm
		ORDER BY req DESC
		LIMIT ?
	`
	err := LOG_DB.WithContext(ctx).Raw(query, from, to, likeModel(modelFilter), limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]ErrorCluster, 0, len(rows))
	for _, r := range rows {
		fp := r.Fingerprint
		if fp == "" {
			fp = "(empty message)"
		}
		out = append(out, ErrorCluster{
			Fingerprint: fp,
			Sample:      r.Sample,
			Requests:    r.Req,
			Users:       r.Users,
			Channels:    r.Channels,
			FirstSeen:   r.FirstSeen,
			LastSeen:    r.LastSeen,
			StatusCodes: countStatusCodeSlice(r.Codes),
		})
	}
	return out, nil
}

// countStatusCodeSlice 统计 ClickHouse groupArray 捞回来的状态码。
//
// groupArray 里空串代表这条错误没有状态码（例如内容里根本没写 status_code），
// 直接跳过——否则会凭空多出一个空码的计数。
func countStatusCodeSlice(codes []string) []StatusCodeCount {
	if len(codes) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, code := range codes {
		if code == "" {
			continue
		}
		counts[code]++
	}
	return sortedStatusCodeCounts(counts)
}

// sortedStatusCodeCounts 把 map 转成稳定排序的切片（次数降序、同次数按码升序）。
func sortedStatusCodeCounts(counts map[string]int) []StatusCodeCount {
	if len(counts) == 0 {
		return nil
	}
	out := make([]StatusCodeCount, 0, len(counts))
	for code, n := range counts {
		out = append(out, StatusCodeCount{Code: code, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Code < out[j].Code
	})
	return out
}
