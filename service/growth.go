package service

/* APIDUO:growth:BEGIN */
// 增长运营的计算层。全部是离线任务或只读派生，不进入任何请求路径。
//
// 数据流只有一条：
//   quota_data（消耗/请求数，小时桶）──┐
//   logs（失败数，按天扫）────────────┼─► user_growth_stats（每用户一行）
//   users（余额/注册时间）────────────┘         │
//                                              ├─► 分层（实时排序）
//                                              ├─► 预警队列（实时规则）
//                                              └─► 概览指标
// 没有第二套事实来源，也不为派生数据单独建表。

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// growthLocation 决定「一天」的边界。默认取进程本地时区；
// 可用 GROWTH_TIMEZONE 覆盖，避免服务器跑 UTC 时日环比偏移 8 小时。
func growthLocation() *time.Location {
	if name := strings.TrimSpace(common.GetEnvOrDefaultString("GROWTH_TIMEZONE", "")); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

// DayStart 返回相对今天偏移 n 天的 0 点时间戳（n 为负数表示过去）。
func DayStart(n int) int64 {
	now := time.Now().In(growthLocation())
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, growthLocation())
	return day.AddDate(0, 0, n).Unix()
}

// WeekStart 返回本周一 0 点的 Unix 时间。
// 周口径必须按自然周对齐：滚动 7 天会让「本周」每天滑动，
// 环比数字永远无法与上周同日对比。
// quotaToUSD 按当前 QuotaPerUnit 折算美元。
// 必须记录换算率：QuotaPerUnit 可配置，改过之后历史 quota 跨期不可比。
func QuotaToUSD(quota int) float64 { return quotaToUSD(quota) }

func quotaToUSD(quota int) float64 {
	if common.QuotaPerUnit <= 0 {
		return 0
	}
	return float64(quota) / common.QuotaPerUnit
}

func WeekStart() int64 {
	return weekStartAt(time.Now().Unix())
}

// weekStartAt 把时间点归到所在自然周的周一 0 点。
// 独立成函数是为了能对不同星期几做断言——周日归到本周一而不是下周一，
// 这是最容易写错的一处。
func weekStartAt(ts int64) int64 {
	t := time.Unix(ts, 0).In(growthLocation())
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7 // 周日算第 7 天，周一才是第 1 天
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, growthLocation())
	return day.AddDate(0, 0, -(weekday - 1)).Unix()
}

// GrowthTasksEnabled 由环境变量控制，默认开启。
func GrowthTasksEnabled() bool {
	return common.GetEnvOrDefaultString("GROWTH_TASKS_DISABLED", "") != "1"
}

// RefreshGrowthStats 重算每个用户的窗口聚合与分层结论。
//
// 三个窗口都从 quota_data 取：
//
//	近 7 天   = usage(>= D-6 00:00)
//	近 30 天  = usage(>= D-29 00:00)
//	前 7 天   = usage(>= D-13 00:00) - usage(>= D-6 00:00)
//
// 消耗是累加量，减法成立；过期数据不会回填，所以不需要区间查询。
func RefreshGrowthStats() error {
	now := time.Now().Unix()
	d3 := DayStart(-2)
	d7 := DayStart(-6)
	d14 := DayStart(-13)
	d30 := DayStart(-29)
	d90 := DayStart(-89)

	// 查询带超时：quota_data 增长后单次聚合可能跑到秒级，卡住会一直占着
	// 一个连接池连接，而转发和它共用同一个池。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 一次扫描算完四个窗口。拆成四次会让近 7 天数据被重复扫四遍。
	rollup, err := model.ScanQuotaRollup(ctx, d3, d7, d14, d30, d90)
	if err != nil {
		return err
	}
	// 一次扫描同时拿到 7 天与 30 天：7 天判当下事故，30 天作这个客户自己的基线。
	failureStats, err := model.ScanFailures(ctx, d7, d30)
	if err != nil {
		return err
	}
	// 模型画像单独一次 30 天扫描，不并进上面那次 90 天聚合。
	profiles, err := model.ScanUserModels(ctx, d30)
	if err != nil {
		return err
	}
	topups, err := model.ScanTopups()
	if err != nil {
		return err
	}
	touches, err := model.ScanLastTouches()
	if err != nil {
		return err
	}
	users, err := model.ListAllUsersBrief()
	if err != nil {
		return err
	}
	userMap := make(map[int]model.UserBrief, len(users))
	for _, u := range users {
		userMap[u.Id] = u
	}

	// 人工字段（manual_* / alert_state / owner_id）在重算时从旧行继承，
	// 保证覆盖操作不会被下一次刷新冲掉。
	existing := map[int]*model.GrowthUserBoard{}
	if old, err := model.ListGrowthBoard(); err == nil {
		for i := range old {
			existing[old[i].UserId] = &old[i]
		}
	}

	rows := make([]model.GrowthUserBoard, 0, len(userMap))
	for id, u := range userMap {
		r := rollup[id]
		row := model.GrowthUserBoard{
			UserId:        id,
			Username:      u.Username,
			DisplayName:   u.DisplayName,
			UserGroup:     u.Group,
			Status:        u.Status,
			CreatedAt:     u.CreatedAt,
			Quota3d:       r.Quota3d,
			Quota7d:       r.Quota7d,
			Quota30d:      r.Quota30d,
			Quota90d:      r.Quota90d,
			Requests7d:    r.Requests7d,
			Requests30d:   r.Requests30d,
			Tokens30d:     r.Tokens30d,
			QuotaPrev7d:   r.Quota14d - r.Quota7d,
			Failures7d:    failureStats[id].Failures7d,
			Failures30d:   failureStats[id].Failures30d,
			Balance:       int64(u.Quota),
			TopupTotal:    topups[id].Total,
			LastTopupAt:   topups[id].LastAt,
			FirstActiveAt: r.FirstAt,
			LastActiveAt:  r.LastAt,
			LastTouchAt:   touches[id].LastAt,
		}
		if row.QuotaPrev7d < 0 {
			row.QuotaPrev7d = 0
		}
		if prof, ok := profiles[id]; ok {
			row.PrimaryModel = prof.PrimaryModel
			row.ModelCount = prof.ModelCount
		}
		if prev, ok := existing[id]; ok {
			// 近 30 天无消耗时不覆盖旧画像，否则一次静默就抹掉全部历史。
			if row.PrimaryModel == "" {
				row.PrimaryModel = prev.PrimaryModel
				row.ModelCount = prev.ModelCount
			}
			if modelSwitch(prev.PrimaryModel, row.PrimaryModel) {
				row.ModelSwitchedAt = now
				row.ModelPrevModel = prev.PrimaryModel
			} else {
				row.ModelSwitchedAt = prev.ModelSwitchedAt
				row.ModelPrevModel = prev.ModelPrevModel
			}
			row.ManualTier = prev.ManualTier
			row.ManualReason = prev.ManualReason
			row.AlertState = prev.AlertState
			row.OwnerId = prev.OwnerId
		}
		rows = append(rows, row)
	}
	settings := LoadGrowthSettings()
	assignTiers(rows, DayStart(-settings.SilentDays+1), settings)
	return model.UpsertGrowthBoard(rows)
}

// GrowthSettings 是分层切点与预警阈值。存在 options 表里，运营可改，
// 改完在下一次重算或手动点「重新计算」后生效。
type GrowthSettings struct {
	TierKeyPercent    int     `json:"tier_key_percent"`
	TierActivePercent int     `json:"tier_active_percent"`
	TierKeyCap        int     `json:"tier_key_cap"`
	SilentDays        int     `json:"silent_days"`
	BalanceWarnDays   float64 `json:"balance_warn_days"`
	IncidentRatePct   float64 `json:"incident_rate_pct"`
	// IncidentFloorPct 是绝对下限，挡住小样本噪声：基线 0.01% 的客户
	// 涨到 0.05%（5 倍）不该报警，那可能只是一个请求失败。
	IncidentFloorPct float64 `json:"incident_floor_pct"`
	// IncidentBaselineMult 是相对基线的倍数。只看绝对值会让常态就高的
	// 客户天天报警、常态极低的客户恶化 80 倍也不报。
	IncidentBaselineMult float64 `json:"incident_baseline_mult"`
	DeclinePct           float64 `json:"decline_pct"`
	DormantDays          int     `json:"dormant_days"`
}

const growthSettingsKey = "growth.settings"

// DefaultGrowthSettings 是首次部署的口径：K 层 3% 保证「能叫出名字」，
// 沉默 30 天对齐一个自然月。
func DefaultGrowthSettings() GrowthSettings {
	return GrowthSettings{
		TierKeyPercent:       3,
		TierActivePercent:    15,
		TierKeyCap:           20,
		SilentDays:           30,
		BalanceWarnDays:      3,
		IncidentRatePct:      20,
		IncidentFloorPct:     0.5,
		IncidentBaselineMult: 3,
		DeclinePct:           10,
		DormantDays:          14,
	}
}

// normalize 把越界值拉回可接受区间，避免填 0 或负数把分层算崩。
func (s GrowthSettings) normalize() GrowthSettings {
	clampInt := func(v, lo, hi int) int {
		return min(max(v, lo), hi)
	}
	s.TierKeyPercent = clampInt(s.TierKeyPercent, 1, 50)
	s.TierActivePercent = clampInt(s.TierActivePercent, s.TierKeyPercent+1, 100)
	s.TierKeyCap = clampInt(s.TierKeyCap, 1, 500)
	s.SilentDays = clampInt(s.SilentDays, 7, 365)
	s.BalanceWarnDays = min(max(s.BalanceWarnDays, 0.5), 30)
	s.IncidentRatePct = min(max(s.IncidentRatePct, 1), 100)
	s.IncidentFloorPct = min(max(s.IncidentFloorPct, 0), 50)
	s.IncidentBaselineMult = min(max(s.IncidentBaselineMult, 1.5), 20)
	s.DeclinePct = min(max(s.DeclinePct, 1), 90)
	s.DormantDays = clampInt(s.DormantDays, 1, 180)
	return s
}

// LoadGrowthSettings 读取配置，未设置或解析失败时回落默认值。
func LoadGrowthSettings() GrowthSettings {
	defaults := DefaultGrowthSettings()
	raw, found, err := model.GetOptionRaw(growthSettingsKey)
	if err != nil || !found || raw == "" {
		return defaults
	}
	var s GrowthSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return defaults
	}
	return s.normalize()
}

// SaveGrowthSettings 校验并落库。
func SaveGrowthSettings(s GrowthSettings) (GrowthSettings, error) {
	s = s.normalize()
	raw, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	return s, model.SetOptionRaw(growthSettingsKey, string(raw))
}

// percentileCuts 返回 K 层与 A 层的排位切点。人数少时保底一个 K。
func percentileCuts(total int, s GrowthSettings) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	keyCut := total * s.TierKeyPercent / 100
	if keyCut < 1 {
		keyCut = 1
	}
	if keyCut > s.TierKeyCap {
		keyCut = s.TierKeyCap
	}
	activeCut := total * s.TierActivePercent / 100
	if activeCut <= keyCut {
		activeCut = keyCut + 1
	}
	if activeCut > total {
		activeCut = total
	}
	return keyCut, activeCut
}

// assignTiers 只对有近期成功调用的用户做分位排名；30 天无调用的一律归沉默，
// 不参与排名，否则沉默账户会把 K 层切点挤乱。
func assignTiers(rows []model.GrowthUserBoard, silentBefore int64, settings GrowthSettings) {
	active := make([]int, 0, len(rows))
	for i := range rows {
		if rows[i].LastActiveAt > 0 && rows[i].LastActiveAt >= silentBefore {
			active = append(active, i)
			continue
		}
		rows[i].Tier = model.GrowthTierSilent
		rows[i].Trend = model.GrowthTrendNone
	}
	sort.Slice(active, func(a, b int) bool {
		return rows[active[a]].Quota30d > rows[active[b]].Quota30d
	})
	keyCut, activeCut := percentileCuts(len(active), settings)
	for rank, idx := range active {
		switch {
		case rank < keyCut:
			rows[idx].Tier = model.GrowthTierKey
		case rank < activeCut:
			rows[idx].Tier = model.GrowthTierActive
		default:
			rows[idx].Tier = model.GrowthTierLong
		}
		rows[idx].Trend = trendOf(rows[idx].Quota7d, rows[idx].QuotaPrev7d, settings.DeclinePct/100)
	}
}

func trendOf(last7, prev7 int, threshold float64) string {
	switch {
	case last7 == 0 && prev7 == 0:
		return model.GrowthTrendNone
	case prev7 == 0:
		return model.GrowthTrendUp
	}
	change := float64(last7-prev7) / float64(prev7)
	switch {
	case change > threshold:
		return model.GrowthTrendUp
	case change < -threshold:
		return model.GrowthTrendDown
	default:
		return model.GrowthTrendFlat
	}
}

// isIncident 判断「有请求但零消耗」。这是把事故型和沉默区分开的唯一依据：
// 沉默是没来调用，事故是来了但全部失败，处置完全不同。
func isIncident(b model.GrowthUserBoard) bool {
	return b.Requests7d > 0 && b.Quota7d == 0
}

// failureRatio 算失败占比。分母是「成功 + 失败」，不是只算成功——
// 只算成功会让重试多的场景失真。
func failureRatio(failures, requests int) float64 {
	total := failures + requests
	if total <= 0 {
		return 0
	}
	return float64(failures) / float64(total)
}

// isFailureSpike 判断近期失败率是否构成事故。
// 三条判据任一成立即触发，但都必须先过绝对下限：
//  1. 超过绝对阈值（常态就高但突然爆表）；
//  2. 超过自身 30 天基线的 N 倍（常态极低但显著恶化）。
//
// 为什么必须有下限：基线 0.01% 的客户，一个请求失败就会让比率翻几十倍，
// 那种"相对恶化"是噪声不是事故。
func isFailureSpike(ratio7, ratio30 float64, s GrowthSettings) bool {
	floor := s.IncidentFloorPct / 100
	if ratio7 < floor {
		return false
	}
	if ratio7 >= s.IncidentRatePct/100 {
		return true
	}
	// 没有基线（新客户、窗口内首次出现失败）时，用 floor/mult 作等效基线，
	// 效果就是「过了下限即告警」。新客户没有历史可比，宁可让他上一次队列，
	// 也不要因为"没有基线"而放过一个真实的 3% 失败率。
	baseline := ratio30
	if baseline <= 0 {
		baseline = floor / s.IncidentBaselineMult
	}
	return ratio7 >= baseline*s.IncidentBaselineMult
}

// 预警类型。与前端模板绑定。
const (
	GrowthAlertBalance     = "balance"
	GrowthAlertExhausted   = "exhausted"
	GrowthAlertModelSwitch = "model_switch"
	GrowthAlertIncident    = "incident"
	GrowthAlertDecline     = "decline"
	GrowthAlertDormant     = "dormant"
)

// BuildGrowthAlerts 按当前聚合行实时算出预警队列。
//
// 最关键的一条是「有请求但无消耗 → 事故型」：quota_data 只记成功消费，
// 一个被故障打死的客户消耗为 0，只看消耗会被判成沉默，而它恰恰是最该救的。
func BuildGrowthAlerts(includeClosed bool) ([]model.GrowthAlertItem, error) {
	stats, err := model.ListGrowthBoard()
	if err != nil {
		return nil, err
	}
	users, err := model.ListAllUsersBrief()
	if err != nil {
		return nil, err
	}
	quotas := make(map[int]int, len(users))
	status := make(map[int]int, len(users))
	createdAt := make(map[int]int64, len(users))
	names := make(map[int]string, len(users))
	for _, u := range users {
		quotas[u.Id] = u.Quota
		status[u.Id] = u.Status
		createdAt[u.Id] = u.CreatedAt
		names[u.Id] = u.Username
	}

	settings := LoadGrowthSettings()
	now := time.Now().Unix()
	dormantCutoff := now - int64(settings.DormantDays)*86400
	silentCutoff := now - int64(settings.SilentDays)*86400
	out := make([]model.GrowthAlertItem, 0, 32)
	for i := range stats {
		s := stats[i]
		if status[s.UserId] != common.UserStatusEnabled {
			continue
		}
		// 用 3 天速率与 7 天速率取大者：客户放量时 7 天均值滞后，
		// 只按它算会报「还有 5 天」而实际明天就断。
		daily := float64(s.Quota7d) / 7.0
		if rate3 := float64(s.Quota3d) / 3.0; rate3 > daily {
			daily = rate3
		}

		// 一个人可能同时命中多类问题，组与组之间不再互斥。
		// 余额将尽与刚换模型是两件完全不同的事：前者要催充值，后者要问原因。
		// 用单层 switch 会让「正在准备离开」的客户只显示「余额不足」，最稀缺的信号被吞掉。
		emit := func(kind string, severity int, detail string, loss int) {
			mark := s.AlertStatus(kind)
			if mark == "" {
				mark = model.GrowthAlertOpen
			}
			out = append(out, model.GrowthAlertItem{
				UserId:      s.UserId,
				Username:    names[s.UserId],
				Quota:       quotas[s.UserId],
				Tier:        s.EffectiveTier(),
				TriggeredAt: s.ComputedAt,
				Kind:        kind,
				Severity:    severity,
				Detail:      detail,
				LossQuota:   loss,
				Status:      mark,
			})
		}

		// 第一组（互斥）：当前已经用不了。事故与余额只会命中一个。
		switch {
		case isIncident(s):
			emit(GrowthAlertIncident, 1, "近 7 天有请求但消耗为 0，疑似全部失败", s.QuotaPrev7d)
		case s.Failures7d > 0 && isFailureSpike(
			failureRatio(s.Failures7d, s.Requests7d),
			failureRatio(s.Failures30d, s.Requests30d),
			settings,
		):
			emit(GrowthAlertIncident, 1, incidentDetail(s.Failures7d, s.Requests7d), 0)
		// 耗尽型：进入条件必须与沉默判定的窗口结构性对齐。
		// 用 silentCutoff（走配置）而不是硬编码的 Quota30d——后者只在 silent_days=30 时
		// 才恰好与沉默切点重合。改成 45 会留出十几天的洞，改成 14 会让两个集合重叠。
		//   last_active_at 在窗口内 —— 曾是客户；窗口外即归沉默，互斥且无缝
		//   last_touch_at 7 天外     —— 触达过就退出，否则同一条预警会天天挂在队列顶部
		//   worthRescuing            —— 价值闸门，避免试用账户塞满队列
		// 刻意不加「安静 3 天」：last_active_at 取自 quota_data（只有成功消费），
		// 跑空后每天被拦截的客户，这个字段冻结在最后一次成功的时刻，判不出他在重试。
		case quotas[s.UserId] <= 0 &&
			s.LastActiveAt > 0 &&
			s.LastActiveAt >= silentCutoff &&
			(s.LastTouchAt == 0 || s.LastTouchAt < now-7*86400) &&
			worthRescuing(s):
			emit(GrowthAlertExhausted, 1, exhaustedDetail(), int(exhaustedRate(s)*14))
		case daily > 0 && float64(quotas[s.UserId])/daily < settings.BalanceWarnDays:
			emit(GrowthAlertBalance, 1, balanceDetail(float64(quotas[s.UserId])/daily), int(daily*14))
		}

		// 第二组（独立维度）：换模型是流失的领先信号，与上面任何一条都不冲突。
		// 消耗量是滞后指标——客户换模型时总消耗可能还在涨，但那已经是离开的第一步。
		if s.ModelSwitchedAt > 0 && s.ModelSwitchedAt >= now-7*86400 && s.PrimaryModel != "" {
			emit(GrowthAlertModelSwitch, 2, switchDetail(s.ModelPrevModel, s.PrimaryModel), s.QuotaPrev7d)
		}

		// 第三组（互斥）：量在往下走。已经沉默的不再报下滑，否则同一个人挂两条。
		switch {
		case s.EffectiveTier() != model.GrowthTierSilent && s.Trend == model.GrowthTrendDown:
			emit(GrowthAlertDecline, 2, declineDetail(s.Quota7d, s.QuotaPrev7d), s.QuotaPrev7d-s.Quota7d)
		// 必须同时排除有失败记录的情况：失败说明他在调用，只是全失败了，
		// 那是事故型的活。不排除会让同一个问题从两个角度各报一次，
		// 运营看到的「无成功调用」掩盖了真实原因。
		case createdAt[s.UserId] > 0 && createdAt[s.UserId] < dormantCutoff &&
			s.Requests30d == 0 && s.Failures7d == 0:
			emit(GrowthAlertDormant, 3, "注册超过 14 天且无成功调用", 0)
		}
	}

	// 按预计损失倒序，而不是严重度 + user_id。运营的一天应该按钱排序：
	// 降 80% 的中型客户可能比降 12% 的大客户更值得先打。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LossQuota != out[j].LossQuota {
			return out[i].LossQuota > out[j].LossQuota
		}
		if out[i].Severity != out[j].Severity {
			return out[i].Severity < out[j].Severity
		}
		return out[i].UserId < out[j].UserId
	})
	return out, nil
}

func worthRescuing(b model.GrowthUserBoard) bool {
	if b.TopupTotal > 0 {
		return true
	}
	tier := b.EffectiveTier()
	return tier == model.GrowthTierKey || tier == model.GrowthTierActive
}

// exhaustedRate 估算耗尽型账户的日均消耗。
// 先取 7 天，为 0 时回落 30 天——否则耗尽第 8 天起 Quota7d 归零，
// 连损失金额都会跟着算成 0，队列里就彻底看不见了。
func exhaustedRate(b model.GrowthUserBoard) float64 {
	if b.Quota7d > 0 {
		return float64(b.Quota7d) / 7
	}
	return float64(b.Quota30d) / 30
}

// modelSwitch 判断主用模型是否发生变化。
// 两个例外必须排除：首次建立画像（空→有）不是切换，
// 画像丢失（有→空，通常是窗口内无消耗）也不是切换。
func modelSwitch(prevPrimary, nextPrimary string) bool {
	if nextPrimary == "" || prevPrimary == "" {
		return false
	}
	return !strings.EqualFold(prevPrimary, nextPrimary)
}

// switchDetail 生成换模型预警的文案，必须带上前后两个模型——
// 只说「换了模型」运营无法判断这是升级还是准备离开。
func switchDetail(prev, next string) string {
	if prev == "" {
		return "主用模型变为 " + next + "，此前无记录"
	}
	return "主用模型从 " + prev + " 换成 " + next + "，留意是否在横向对比供应商"
}

func exhaustedDetail() string {
	return "余额已耗尽，但近 7 天仍有调用，下一次请求会失败"
}

func balanceDetail(remainDays float64) string {
	// 速率取 3 天与 7 天较快者，文案不能再写「近 7 天」——
	// 否则运营按 7 天去复核会算不出这个数，以为系统错了。
	return "按近 3/7 天较快速率，余额约 " + strconv.FormatFloat(remainDays, 'f', 1, 64) + " 天后耗尽"
}

func incidentDetail(failures, requests int) string {
	total := failures + requests
	if total == 0 {
		return "请求异常"
	}
	pct := float64(failures) * 100 / float64(total)
	return "失败占比 " + strconv.FormatFloat(pct, 'f', 1, 64) + "%，共 " + strconv.Itoa(failures) + " 次失败"
}

func declineDetail(last7, prev7 int) string {
	return "近 7 天消耗 " + strconv.Itoa(last7) + "，前 7 天 " + strconv.Itoa(prev7) + "，降幅超过 10%"
}

// SettleGrowthBatches 结算到期批次：窗口期消耗对比窗口前等长区间，差值即增量。
// 三次累加量相减即可，不需要按日期分组。
func SettleGrowthBatches() error {
	batches, err := model.ListRunningBatches()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, b := range batches {
		if b.StartedAt == 0 || b.WindowDays <= 0 {
			continue
		}
		span := int64(b.WindowDays) * 86400
		windowEnd := b.StartedAt + span
		if windowEnd > now {
			continue
		}
		ids := model.ParseUserIds(b.TargetUserIds)
		if len(ids) == 0 {
			continue
		}
		cumPrev, err := model.SumQuotaForUsers(ids, b.StartedAt-span)
		if err != nil {
			return err
		}
		cumStart, err := model.SumQuotaForUsers(ids, b.StartedAt)
		if err != nil {
			return err
		}
		cumEnd, err := model.SumQuotaForUsers(ids, windowEnd)
		if err != nil {
			return err
		}
		prevWindow := cumPrev - cumStart
		currentWindow := cumStart - cumEnd
		incremental := currentWindow - prevWindow
		if incremental < 0 {
			incremental = 0
		}
		roi := 0.0
		if b.Cost > 0 {
			roi = float64(incremental) / float64(b.Cost)
		}
		if err := model.MarkBatchSettled(b.Id, incremental, roi); err != nil {
			return err
		}
	}
	return nil
}

// GrowthOverview 汇总概览页需要的指标。
type GrowthOverview struct {
	// 北极星：周计费消耗额。按自然周对齐，与上周等长天数对比。
	WeekStart         int64          `json:"week_start"`
	WeeklyBilledUSD   float64        `json:"weekly_billed_usd"`
	PrevWeekBilledUSD float64        `json:"prev_week_billed_usd"`
	WeekOverWeekPct   float64        `json:"week_over_week_pct"`
	WeekActiveUsers   int            `json:"week_active_users"`
	MonthActiveUsers  int            `json:"month_active_users"`
	RegisteredUsers   int            `json:"registered_users"`
	PayingUsers       int            `json:"paying_users"`
	Top10Share        float64        `json:"top10_share"`
	TierCounts        map[string]int `json:"tier_counts"`
	TierQuota         map[string]int `json:"tier_quota"`
	DataFreshAt       int64          `json:"data_fresh_at"`
}

// BuildGrowthOverview 只读聚合行，不再扫明细表。
func BuildGrowthOverview() (*GrowthOverview, error) {
	stats, err := model.ListGrowthBoard()
	if err != nil {
		return nil, err
	}
	out := &GrowthOverview{
		TierCounts: map[string]int{},
		TierQuota:  map[string]int{},
	}
	// 北极星：本周至今 vs 上周同期（等长天数，否则周一和周日不可比）。
	weekStart := WeekStart()
	nowTs := time.Now().Unix()
	elapsed := nowTs - weekStart
	if elapsed < 1 {
		elapsed = 1
	}
	prevStart := weekStart - 7*86400
	if cur, err := model.ScanBilledQuota(weekStart, nowTs+1); err == nil {
		out.WeekStart = weekStart
		out.WeeklyBilledUSD = quotaToUSD(cur)
	}
	if prev, err := model.ScanBilledQuota(prevStart, prevStart+elapsed); err == nil {
		out.PrevWeekBilledUSD = quotaToUSD(prev)
		if out.PrevWeekBilledUSD > 0 {
			out.WeekOverWeekPct = (out.WeeklyBilledUSD - out.PrevWeekBilledUSD) / out.PrevWeekBilledUSD * 100
		}
	}

	quotas := make([]int, 0, len(stats))
	totalQuota := 0
	for i := range stats {
		s := stats[i]
		if s.Requests30d > 0 {
			out.MonthActiveUsers++
		}
		if s.Requests7d > 0 {
			out.WeekActiveUsers++
		}
		quotas = append(quotas, s.Quota30d)
		totalQuota += s.Quota30d
		tier := s.EffectiveTier()
		if tier != "" {
			out.TierCounts[tier]++
			out.TierQuota[tier] += s.Quota30d
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(quotas)))
	top10 := 0
	for i, q := range quotas {
		if i >= 10 {
			break
		}
		top10 += q
	}
	if totalQuota > 0 {
		out.Top10Share = float64(top10) * 100 / float64(totalQuota)
	}
	if n, err := model.CountUsers(); err == nil {
		out.RegisteredUsers = n
	}
	if n, err := model.CountPayingUsers(); err == nil {
		out.PayingUsers = n
	}
	if fresh, err := model.GrowthDataFreshAt(); err == nil {
		out.DataFreshAt = fresh
	}
	return out, nil
}

// StartGrowthTaskLoop 启动离线任务循环。
// 首次启动立即重算一次，之后每小时检查：跨天后补算，周一额外重算分层。
func StartGrowthTaskLoop() {
	if !GrowthTasksEnabled() {
		common.SysLog(common.LogText("growth tasks disabled"))
		return
	}
	// 只有主节点跑聚合。多实例各自开一套任务会 N 倍抢占连接池，
	// 而转发与它共用同一个池。
	if !common.IsMasterNode {
		common.SysLog(common.LogText("growth tasks skipped on non-master node"))
		return
	}
	go func() {
		if err := RefreshGrowthStats(); err != nil {
			common.SysLog(common.LogText("growth initial refresh failed: %s", err.Error()))
		}
		lastDay := dayMark()
		lastWeek := weekMark()
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now().In(growthLocation())
			if now.Hour() < 4 {
				continue
			}
			if dayMark() == lastDay && weekMark() == lastWeek {
				continue
			}
			if err := RefreshGrowthStats(); err != nil {
				common.SysLog(common.LogText("growth refresh failed: %s", err.Error()))
			}
			if err := SettleGrowthBatches(); err != nil {
				common.SysLog(common.LogText("growth batch settle failed: %s", err.Error()))
			}
			lastDay = dayMark()
			lastWeek = weekMark()
		}
	}()
}

func dayMark() string {
	return time.Now().In(growthLocation()).Format("2006-01-02")
}

func weekMark() string {
	y, w := time.Now().In(growthLocation()).ISOWeek()
	return strconv.Itoa(y) + "-" + strconv.Itoa(w)
}

/* APIDUO:growth:END */
