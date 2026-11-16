package model

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

/* APIDUO:growth:BEGIN */
// 用户运营看板只有一张宽表 + 两张业务表。
//
//	GrowthUserBoard  每用户一行的看板主表。分层、今日队列、客户档案头部、
//	                 概览四个页面全部读它，口径只在这里定义一次。
//	GrowthTouch      触达记录。业务事实，删了就没了。
//	GrowthBatch      发放批次。没有批次号的权益发放无法归因，只能算成本。
//
// 三条不能破的约束：
//   1. 粒度是用户级，不是用户×天级。改粒度等于重建，所以一次定死。
//   2. 宽表只能派生，永远可以整表重算覆盖。任何人在上面手改数据都会制造
//      第二个事实来源，和 logs / quota_data 打架。
//   3. 时间序列不进宽表。方向用两个窗口标量表达；要看逐日曲线请查明细。
//
// 任何情况下都不写 users.group，也不改 users 表结构。

const (
	GrowthTierKey     = "K"
	GrowthTierActive  = "A"
	GrowthTierLong    = "B"
	GrowthTierSilent  = "S"
	GrowthTrendUp     = "up"
	GrowthTrendFlat   = "flat"
	GrowthTrendDown   = "down"
	GrowthTrendNone   = "none"
	GrowthAlertOpen   = "open"
	GrowthAlertDone   = "done"
	GrowthAlertIgnore = "ignore"
)

// GrowthUserBoard 是用户运营看板的唯一主表。
// 列按「身份 → 规模 → 趋势 → 质量 → 生命周期 → 分层 → 运营状态」分组，
// 新增指标优先往对应组里加列，不要另起一张表。
type GrowthUserBoard struct {
	// 身份
	UserId      int    `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	Username    string `json:"username" gorm:"size:64;default:''"`
	DisplayName string `json:"display_name" gorm:"size:64;default:''"`
	UserGroup   string `json:"user_group" gorm:"size:64;default:''"`
	Status      int    `json:"status" gorm:"default:0"`
	CreatedAt   int64  `json:"created_at" gorm:"default:0"`

	// 规模
	// Quota3d 供余额「还能撑几天」用。7 天均值在客户放量时严重滞后，
	// 报「还有 5 天」其实明天就断。
	Quota3d     int `json:"quota_3d" gorm:"column:quota_3d;default:0"`
	Quota7d     int `json:"quota_7d" gorm:"column:quota_7d;default:0"`
	Quota30d    int `json:"quota_30d" gorm:"column:quota_30d;default:0;index:idx_gub_quota30,priority:1;index:idx_gub_tier_quota,priority:2"`
	Quota90d    int `json:"quota_90d" gorm:"column:quota_90d;default:0"`
	Requests7d  int `json:"requests_7d" gorm:"column:requests_7d;default:0"`
	Requests30d int `json:"requests_30d" gorm:"column:requests_30d;default:0"`
	Tokens30d   int `json:"tokens_30d" gorm:"column:tokens_30d;default:0"`

	// 趋势
	QuotaPrev7d int    `json:"quota_prev_7d" gorm:"column:quota_prev_7d;default:0"`
	Trend       string `json:"trend" gorm:"size:8;default:''"`

	// 模型画像：主用模型与切换痕迹。
	// 消耗量是滞后指标——客户换模型时总消耗可能还在涨，
	// 但那已经是离开的第一步，这条记录是唯一能提前看到的信号。
	PrimaryModel    string `json:"primary_model" gorm:"column:primary_model;size:64;default:''"`
	ModelCount      int    `json:"model_count" gorm:"column:model_count;default:0"`
	ModelSwitchedAt int64  `json:"model_switched_at" gorm:"column:model_switched_at;default:0"`
	ModelPrevModel  string `json:"model_prev_model" gorm:"column:model_prev_model;size:64;default:''"`

	// 余额：直接来自 users.quota。不要用「累计充值 − 消耗」推算，
	// 那既不是余额也不准确——赠送额度、退款、负额度都会让推算偏离。
	Balance int64 `json:"balance" gorm:"column:balance;default:0"`

	// 质量
	Failures7d  int   `json:"failures_7d" gorm:"column:failures_7d;default:0"`
	Failures30d int   `json:"failures_30d" gorm:"column:failures_30d;default:0"`
	TopupTotal  int   `json:"topup_total" gorm:"default:0"`
	LastTopupAt int64 `json:"last_topup_at" gorm:"default:0"`

	// 生命周期
	FirstActiveAt int64 `json:"first_active_at" gorm:"default:0"`
	LastActiveAt  int64 `json:"last_active_at" gorm:"default:0;index"`

	// 分层
	Tier         string `json:"tier" gorm:"size:8;default:'';index:idx_gub_tier,priority:1;index:idx_gub_tier_quota,priority:1"`
	ManualTier   string `json:"manual_tier" gorm:"size:8;default:''"`
	ManualReason string `json:"manual_reason" gorm:"size:255;default:''"`

	// 运营状态
	OwnerId     int    `json:"owner_id" gorm:"default:0"`
	LastTouchAt int64  `json:"last_touch_at" gorm:"default:0"`
	AlertState  string `json:"-" gorm:"type:text"`
	ComputedAt  int64  `json:"computed_at" gorm:"default:0;index"`
}

func (GrowthUserBoard) TableName() string { return "growth_user_board" }

// 注意：字段名里的数字不会让 GORM 插入下划线。Quota30d 的默认列名是
// quota30d 而不是 quota_30d，所以每个带数字的列都必须显式写 column 标签，
// 否则 Order / Select / AssignmentColumns 里用到的 quota_30d 会直接报
// 「Unknown column」。这个坑在测试里不会暴露，只在真实 SQL 上炸。

// EffectiveTier 返回对外生效的层级：人工覆盖优先于机器判定。
func (b *GrowthUserBoard) EffectiveTier() string {
	if b.ManualTier != "" {
		return b.ManualTier
	}
	return b.Tier
}

// AlertMark 是单条预警的人工状态。
type AlertMark struct {
	Status string `json:"status"`
	At     int64  `json:"at"`
}

// AlertStatus 读取某类型预警的人工状态，未标记返回空串。
func (b *GrowthUserBoard) AlertStatus(kind string) string {
	if b.AlertState == "" {
		return ""
	}
	var marks map[string]AlertMark
	if err := json.Unmarshal([]byte(b.AlertState), &marks); err != nil {
		return ""
	}
	return marks[kind].Status
}

// MarkAlert 返回写入某类型预警状态后的 JSON。
func (b *GrowthUserBoard) MarkAlert(kind, status string, at int64) string {
	marks := map[string]AlertMark{}
	if b.AlertState != "" {
		_ = json.Unmarshal([]byte(b.AlertState), &marks)
	}
	marks[kind] = AlertMark{Status: status, At: at}
	raw, err := json.Marshal(marks)
	if err != nil {
		return b.AlertState
	}
	return string(raw)
}

// GrowthTouch 人工触达记录。只增不删：删除记录等于销毁客户关系史。
type GrowthTouch struct {
	Id           int    `json:"id"`
	UserId       int    `json:"user_id" gorm:"index"`
	Method       string `json:"method" gorm:"size:16;default:''"`
	ContactRole  string `json:"contact_role" gorm:"size:16;default:''"`
	Feedback     string `json:"feedback" gorm:"type:text"`
	Conclusion   string `json:"conclusion" gorm:"size:16;default:''"`
	FollowUpAt   int64  `json:"follow_up_at" gorm:"default:0;index"`
	OperatorId   int    `json:"operator_id" gorm:"default:0"`
	OperatorName string `json:"operator_name" gorm:"size:64;default:''"`
	CreatedAt    int64  `json:"created_at" gorm:"index"`
}

func (GrowthTouch) TableName() string { return "growth_touches" }

// GrowthBatch 发放批次。
type GrowthBatch struct {
	Id               int     `json:"id"`
	BatchNo          string  `json:"batch_no" gorm:"uniqueIndex;size:32"`
	Name             string  `json:"name" gorm:"size:128;default:''"`
	Criteria         string  `json:"criteria" gorm:"size:255;default:''"`
	BenefitType      string  `json:"benefit_type" gorm:"size:32;default:''"`
	BenefitAmount    int     `json:"benefit_amount" gorm:"default:0"`
	TargetCount      int     `json:"target_count" gorm:"default:0"`
	TargetUserIds    string  `json:"-" gorm:"type:text"`
	Cost             int     `json:"cost" gorm:"default:0"`
	WindowDays       int     `json:"window_days" gorm:"default:14"`
	IncrementalQuota int     `json:"incremental_quota" gorm:"default:0"`
	Roi              float64 `json:"roi" gorm:"default:0"`
	Status           string  `json:"status" gorm:"size:16;default:'running'"`
	StartedAt        int64   `json:"started_at" gorm:"default:0"`
	SettledAt        int64   `json:"settled_at" gorm:"default:0"`
}

func (GrowthBatch) TableName() string { return "growth_batches" }

// QuotaUsage 是 quota_data 汇总出的一个时间窗口。
type QuotaUsage struct {
	Quota    int
	Requests int
	Tokens   int
	FirstAt  int64
	LastAt   int64
}

// QuotaRollup 把四个窗口压进一次扫描。
// 拆成四次查询会让 7 天数据被重复扫四遍——窗口是嵌套的，没必要。
type QuotaRollup struct {
	Quota3d     int
	Quota7d     int
	Quota14d    int
	Quota30d    int
	Quota90d    int
	Requests7d  int
	Requests30d int
	Tokens30d   int
	FirstAt     int64
	LastAt      int64
}

// ScanQuotaRollup 用一次索引范围扫描算完 7/14/30/90 天四个窗口。
// 所有窗口共用同一个 WHERE，差异用 CASE WHEN 在聚合里表达。
func ScanQuotaRollup(ctx context.Context, d3, d7, d14, d30, d90 int64) (map[int]QuotaRollup, error) {
	out := map[int]QuotaRollup{}
	if !DB.Migrator().HasTable("quota_data") {
		return out, nil
	}
	// 字段名里带数字时 GORM 的 snake_case 会生成 quota7d 而不是 quota_7d，
	// 所以这里显式指定列名，否则聚合结果会被静默扫成零值。
	type row struct {
		UserId      int
		Quota3d     int   `gorm:"column:quota_3d"`
		Quota7d     int   `gorm:"column:quota_7d"`
		Quota14d    int   `gorm:"column:quota_14d"`
		Quota30d    int   `gorm:"column:quota_30d"`
		Quota90d    int   `gorm:"column:quota_90d"`
		Requests7d  int   `gorm:"column:requests_7d"`
		Requests30d int   `gorm:"column:requests_30d"`
		Tokens30d   int   `gorm:"column:tokens_30d"`
		FirstAt     int64 `gorm:"column:first_at"`
		LastAt      int64 `gorm:"column:last_at"`
	}
	var rows []row
	err := DB.WithContext(ctx).Table("quota_data").
		Select(`user_id,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN quota ELSE 0 END), 0) AS quota_3d,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN quota ELSE 0 END), 0) AS quota_7d,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN quota ELSE 0 END), 0) AS quota_14d,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN quota ELSE 0 END), 0) AS quota_30d,
			COALESCE(SUM(quota), 0) AS quota_90d,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN count ELSE 0 END), 0) AS requests_7d,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN count ELSE 0 END), 0) AS requests_30d,
			COALESCE(SUM(CASE WHEN created_at >= ? THEN token_used ELSE 0 END), 0) AS tokens_30d,
			MIN(created_at) AS first_at,
			MAX(created_at) AS last_at`,
			d3, d7, d14, d30, d7, d30, d30).
		Where("created_at >= ?", d90).
		Group("user_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.UserId] = QuotaRollup{
			Quota3d:     r.Quota3d,
			Quota7d:     r.Quota7d,
			Quota14d:    r.Quota14d,
			Quota30d:    r.Quota30d,
			Quota90d:    r.Quota90d,
			Requests7d:  r.Requests7d,
			Requests30d: r.Requests30d,
			Tokens30d:   r.Tokens30d,
			FirstAt:     r.FirstAt,
			LastAt:      r.LastAt,
		}
	}
	return out, nil
}

// ScanFailures 从 logs 汇总失败请求数。quota_data 只记成功消费，
// 失败数必须回明细表取，否则「有请求无消耗」的事故型用户会被判成沉默。
// FailureStat 是单个用户在两个窗口内的失败请求数。
// 同时保留 7 天与 30 天：7 天用于判断当下是否有事故，
// 30 天作为这个客户自己的常态基线——只看绝对阈值会两头失真。
type FailureStat struct {
	Failures7d  int
	Failures30d int
}

func ScanFailures(ctx context.Context, d7, d30 int64) (map[int]FailureStat, error) {
	out := map[int]FailureStat{}
	conn := LOG_DB
	if conn == nil {
		conn = DB
	}

	// 分两次查而不是用 CASE WHEN：GORM 的 Select 里带 ? 时参数不会自动绑定，
	// 漏传会让条件恒为假（踩过一次，7 天失败数永远是 0）。
	// 日志在 ClickHouse，多一次范围扫描的成本可以忽略。
	scan := func(since int64) (map[int]int, error) {
		type row struct {
			UserId int
			Cnt    int
		}
		var rows []row
		err := conn.WithContext(ctx).Model(&Log{}).
			// 必须按 request_id 去重。日志是按「渠道尝试」写的：
			// 一次客户请求重试 3 个渠道会留 3 行。若用 COUNT(*)，这个请求会同时
			// 计进分母的「1 次成功」和分子的「2 次失败」，失败率被放大 1.8–2.7 倍。
			Select("user_id, COUNT(DISTINCT request_id) AS cnt").
			Where("created_at >= ? AND type = ? AND request_id <> ''", since, LogTypeError).
			Group("user_id").
			Scan(&rows).Error
		m := make(map[int]int, len(rows))
		for _, r := range rows {
			m[r.UserId] = r.Cnt
		}
		return m, err
	}

	recent, err := scan(d7)
	if err != nil {
		return nil, err
	}
	baseline, err := scan(d30)
	if err != nil {
		return nil, err
	}
	for uid, n := range baseline {
		out[uid] = FailureStat{Failures7d: recent[uid], Failures30d: n}
	}
	return out, nil
}

// TopupSum 是用户累计充值。
type TopupSum struct {
	Total  int
	LastAt int64
}

// ScanTopups 汇总成功充值记录。
func ScanTopups() (map[int]TopupSum, error) {
	out := map[int]TopupSum{}
	type row struct {
		UserId int
		Total  int
		LastAt int64
	}
	var rows []row
	err := DB.Model(&TopUp{}).
		Select("user_id, COALESCE(SUM(amount), 0) AS total, MAX(complete_time) AS last_at").
		Where("complete_time > 0").
		Group("user_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.UserId] = TopupSum{Total: r.Total, LastAt: r.LastAt}
	}
	return out, nil
}

// TouchSum 是用户最近一次人工触达。
type TouchSum struct {
	LastAt int64
}

// ScanLastTouches 汇总每个用户最近一次触达时间。
func ScanLastTouches() (map[int]TouchSum, error) {
	out := map[int]TouchSum{}
	type row struct {
		UserId int
		LastAt int64
	}
	var rows []row
	err := DB.Table("growth_touches").
		Select("user_id, MAX(created_at) AS last_at").
		Group("user_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.UserId] = TouchSum{LastAt: r.LastAt}
	}
	return out, nil
}

// UpsertGrowthBoard 整表覆盖式写入，保留人工字段。
func UpsertGrowthBoard(rows []GrowthUserBoard) error {
	if len(rows) == 0 {
		return nil
	}
	now := time.Now().Unix()
	for i := range rows {
		rows[i].ComputedAt = now
	}
	return DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"username", "display_name", "user_group", "status", "created_at",
			"quota_3d", "quota_7d", "quota_30d", "quota_90d",
			"requests_7d", "requests_30d", "tokens_30d",
			"quota_prev_7d", "trend", "failures_7d", "failures_30d", "balance", "primary_model", "model_count", "model_switched_at", "model_prev_model",
			"topup_total", "last_topup_at",
			"first_active_at", "last_active_at",
			"tier", "last_touch_at", "computed_at",
		}),
	}).CreateInBatches(rows, 200).Error
}

// ListGrowthBoard 读取整张看板表，按 30 天消耗倒序。
func ListGrowthBoard() ([]GrowthUserBoard, error) {
	var rows []GrowthUserBoard
	err := DB.Order("quota_30d DESC, user_id ASC").Find(&rows).Error
	return rows, err
}

// GetGrowthBoardRow 读取单个用户看板行。
func GetGrowthBoardRow(userId int) (*GrowthUserBoard, error) {
	var row GrowthUserBoard
	err := DB.Where("user_id = ?", userId).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SetManualTier 设置或清除人工层级覆盖。
func SetManualTier(userId int, tier, reason string) error {
	var row GrowthUserBoard
	err := DB.Where("user_id = ?", userId).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return DB.Create(&GrowthUserBoard{
			UserId:       userId,
			ManualTier:   tier,
			ManualReason: reason,
			ComputedAt:   time.Now().Unix(),
		}).Error
	}
	if err != nil {
		return err
	}
	return DB.Model(&GrowthUserBoard{}).Where("user_id = ?", userId).
		Updates(map[string]any{
			"manual_tier":   tier,
			"manual_reason": reason,
		}).Error
}

// MarkBoardTouched 把最近触达时间往前推，让 7 天抑制立刻生效，
// 不必等下一次聚合刷新。
func MarkBoardTouched(userId int, at int64) error {
	return DB.Model(&GrowthUserBoard{}).Where("user_id = ?", userId).
		Update("last_touch_at", at).Error
}

// SetAlertMark 更新某用户某类型预警的人工状态。
func SetAlertMark(userId int, kind, status string) error {
	board, err := GetGrowthBoardRow(userId)
	if err != nil {
		board = &GrowthUserBoard{UserId: userId}
	}
	next := board.MarkAlert(kind, status, time.Now().Unix())
	if err == gorm.ErrRecordNotFound {
		board.AlertState = next
		return DB.Create(board).Error
	}
	return DB.Model(&GrowthUserBoard{}).Where("user_id = ?", userId).
		Update("alert_state", next).Error
}

// ListGrowthBoardPage 分页查看板行，供列表页使用。
func ListGrowthBoardPage(tier, keyword, sortBy string, desc bool, offset, limit int) ([]GrowthUserBoard, int64, error) {
	q := DB.Model(&GrowthUserBoard{})
	if keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("username LIKE ? OR display_name LIKE ?", like, like)
	}
	switch tier {
	case GrowthTierKey, GrowthTierActive, GrowthTierLong, GrowthTierSilent:
		q = q.Where("COALESCE(NULLIF(manual_tier, ''), tier) = ?", tier)
	case "none":
		q = q.Where("tier = '' AND (manual_tier = '' OR manual_tier IS NULL)")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	dir := "DESC"
	if !desc {
		dir = "ASC"
	}
	var rows []GrowthUserBoard
	err := q.Order(boardSortColumn(sortBy) + " " + dir).Order("user_id ASC").
		Offset(offset).Limit(limit).Find(&rows).Error
	return rows, total, err
}

func boardSortColumn(key string) string {
	switch key {
	case "last_active_at":
		return "last_active_at"
	case "created_at":
		return "created_at"
	case "user_id":
		return "user_id"
	default:
		return "quota_30d"
	}
}

// GrowthDataFreshAt 返回看板最后一次刷新时间，用于界面提示数据新鲜度。
// data_export 被关闭时不写新行，这个时间会停住，界面据此提示「数据不新鲜」。
func GrowthDataFreshAt() (int64, error) {
	var fresh int64
	err := DB.Model(&GrowthUserBoard{}).Select("COALESCE(MAX(computed_at), 0)").Scan(&fresh).Error
	return fresh, err
}

// UserModelProfile 是单个用户的模型使用画像。
type UserModelProfile struct {
	PrimaryModel string // 近 30 天消耗最大的模型
	ModelCount   int    // 用了几种模型
	TopQuota     int    // 主模型的消耗
	TotalQuota   int    // 全部模型合计
}

// ModelStat 是全局单模型的聚合结果。
type ModelStat struct {
	ModelName string `json:"model_name"`
	Quota     int    `json:"quota"`
	Users     int    `json:"users"`
	Requests  int    `json:"requests"`
}

// ScanUserModels 按用户 × 模型聚合，取每人的主用模型。
// 单独一次 30 天扫描，不塞进 90 天的主聚合——那会让每日全量负担翻倍。
func ScanUserModels(ctx context.Context, since int64) (map[int]UserModelProfile, error) {
	out := map[int]UserModelProfile{}
	if !DB.Migrator().HasTable("quota_data") {
		return out, nil
	}
	var rows []struct {
		UserId    int    `gorm:"column:user_id"`
		ModelName string `gorm:"column:model_name"`
		Quota     int    `gorm:"column:quota"`
	}
	err := DB.WithContext(ctx).Table("quota_data").
		Select("user_id, model_name, COALESCE(SUM(quota), 0) AS quota").
		Where("created_at >= ? AND model_name <> ''", since).
		Group("user_id, model_name").
		Scan(&rows).Error
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		p := out[r.UserId]
		p.ModelCount++
		p.TotalQuota += r.Quota
		if r.Quota > p.TopQuota {
			p.TopQuota = r.Quota
			p.PrimaryModel = r.ModelName
		}
		out[r.UserId] = p
	}
	return out, nil
}

// ScanModelBreakdown 按模型聚合消耗、去重用户数与请求数，按消耗倒序。
func ScanModelBreakdown(ctx context.Context, since int64) ([]ModelStat, error) {
	if !DB.Migrator().HasTable("quota_data") {
		return nil, nil
	}
	var out []ModelStat
	err := DB.WithContext(ctx).Table("quota_data").
		Select("model_name, COALESCE(SUM(quota), 0) AS quota, COUNT(DISTINCT user_id) AS users, COALESCE(SUM(count), 0) AS requests").
		Where("created_at >= ? AND model_name <> ''", since).
		Group("model_name").
		Order("quota DESC").
		Scan(&out).Error
	return out, err
}

// ScanBilledQuota 统计 [since, until) 区间内的计费消耗总额。
// 走 created_at 索引范围扫描，只用于管理端概览，不在转发路径上。
func ScanBilledQuota(since, until int64) (int, error) {
	if until <= since {
		return 0, nil
	}
	if !DB.Migrator().HasTable("quota_data") {
		return 0, nil
	}
	var total int64
	err := DB.Table("quota_data").
		Select("COALESCE(SUM(quota), 0)").
		Where("created_at >= ? AND created_at < ?", since, until).
		Scan(&total).Error
	return int(total), err
}

// GetGrowthBoardsByUserIds 批量取看板行，供用户列表页附加派生列，
// 避免每行一次查询。
func GetGrowthBoardsByUserIds(ids []int) (map[int]GrowthUserBoard, error) {
	out := map[int]GrowthUserBoard{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []GrowthUserBoard
	if err := DB.Where("user_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.UserId] = r
	}
	return out, nil
}

// ListDueFollowUps 返回到期未跟进的触达。
// 判据是「每个用户最新一条触达的 follow_up_at 已过」：旧记录被新记录覆盖，
// 所以只要该用户后来又记了一条，旧承诺就不会再冒出来。
func ListDueFollowUps(now int64, limit int) ([]GrowthTouch, error) {
	if limit <= 0 {
		limit = 50
	}
	type row struct {
		Id int
	}
	var ids []row
	err := DB.Table("growth_touches AS t").
		Select("t.id").
		Joins("JOIN (SELECT user_id, MAX(id) AS max_id FROM growth_touches GROUP BY user_id) m ON m.max_id = t.id").
		Where("t.follow_up_at > 0 AND t.follow_up_at <= ?", now).
		Order("t.follow_up_at ASC").
		Limit(limit).
		Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	idList := make([]int, 0, len(ids))
	for _, r := range ids {
		idList = append(idList, r.Id)
	}
	var rows []GrowthTouch
	if err := DB.Where("id IN ?", idList).Order("follow_up_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetOptionRaw 读取一个配置项，未设置时返回 found=false。
func GetOptionRaw(key string) (value string, found bool, err error) {
	var row Option
	// key 是 SQL 保留字，各方言的引用方式不同，必须走 commonKeyCol
	err = DB.Where(commonKeyCol+" = ?", key).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.Value, true, nil
}

// SetOptionRaw 写入一个配置项，已存在则覆盖。
func SetOptionRaw(key, value string) error {
	return DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&Option{Key: key, Value: value}).Error
}

// StopGrowthBatch 手动停止一个批次：状态置为 stopped，
// 已结算的数据保留，结算任务不会再处理它。
func StopGrowthBatch(id int) error {
	return DB.Model(&GrowthBatch{}).Where("id = ?", id).
		Updates(map[string]any{
			"status":     "stopped",
			"settled_at": time.Now().Unix(),
		}).Error
}

// CountUsers 返回未删除的用户总数。
func CountUsers() (int, error) {
	var count int64
	err := DB.Model(&User{}).Count(&count).Error
	return int(count), err
}

// CountPayingUsers 返回有成功充值记录的用户数。
func CountPayingUsers() (int, error) {
	var count int64
	err := DB.Model(&TopUp{}).Where("complete_time > 0").Distinct("user_id").Count(&count).Error
	return int(count), err
}

// ListAllUsersBrief 一次取出全部用户的最小字段，避免预警判定时 N+1 查询。
func ListAllUsersBrief() ([]UserBrief, error) {
	var rows []UserBrief
	err := DB.Model(&User{}).
		Select("id", "username", "display_name", "group", "quota", "status", "created_at").
		Find(&rows).Error
	return rows, err
}

// UserBrief 是预警判定与看板刷新需要的最小用户快照。
type UserBrief struct {
	Id          int    `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Group       string `json:"group" gorm:"column:group"`
	Quota       int    `json:"quota"`
	Status      int    `json:"status"`
	CreatedAt   int64  `json:"created_at"`
}

// ListUserIdsByTier 返回某层级（人工覆盖优先）的用户 id。传空串返回全部有看板行的用户。
func ListUserIdsByTier(tier string) ([]int, error) {
	var ids []int
	q := DB.Model(&GrowthUserBoard{}).Select("user_id").
		Where("status = ?", 1)
	if tier != "" {
		q = q.Where("COALESCE(NULLIF(manual_tier, ''), tier) = ?", tier)
	}
	err := q.Pluck("user_id", &ids).Error
	return ids, err
}

// GrowthAlertItem 是实时算出来的预警条目，不落库。
// 状态（已处理/已忽略）从 GrowthUserBoard.AlertState 读取。
type GrowthAlertItem struct {
	UserId   int    `json:"user_id"`
	Kind     string `json:"kind"`
	Severity int    `json:"severity"`
	// LossQuota 是这条预警预计造成的额度损失，队列按它倒序。
	// 事故型与耗尽型的「下滑缺口」天然为 0，必须补上潜在损失才能与普通下滑相比。
	LossQuota   int    `json:"loss_quota"`
	Detail      string `json:"detail"`
	TriggeredAt int64  `json:"triggered_at"`
	Status      string `json:"status"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Quota       int    `json:"quota"`
	Tier        string `json:"tier"`
}

// GrowthTimelineEvent 是客户档案里的一个时间点。
type GrowthTimelineEvent struct {
	At     int64  `json:"at"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// BuildGrowthTimeline 合并注册、首次/最近活跃、充值、预警与触达，倒序拼接。
// 刻意不合并全部明细日志：那会让档案页退化成日志浏览器。
func BuildGrowthTimeline(user *User, board *GrowthUserBoard, alerts []GrowthAlertItem, touches []GrowthTouch) []GrowthTimelineEvent {
	out := make([]GrowthTimelineEvent, 0, len(alerts)+len(touches)+3)
	if user != nil && user.CreatedAt > 0 {
		out = append(out, GrowthTimelineEvent{At: user.CreatedAt, Kind: "register", Title: "注册"})
	}
	if board != nil && board.FirstActiveAt > 0 {
		out = append(out, GrowthTimelineEvent{At: board.FirstActiveAt, Kind: "active", Title: "首次成功调用"})
	}
	if board != nil && board.LastTopupAt > 0 {
		out = append(out, GrowthTimelineEvent{
			At:     board.LastTopupAt,
			Kind:   "topup",
			Title:  "最近一次充值",
			Detail: "累计 " + strconv.Itoa(board.TopupTotal),
		})
	}
	if board != nil && board.LastActiveAt > 0 {
		out = append(out, GrowthTimelineEvent{
			At:     board.LastActiveAt,
			Kind:   "active",
			Title:  "最近一次成功调用",
			Detail: "近 30 天 " + strconv.Itoa(board.Requests30d) + " 次请求",
		})
	}
	for _, a := range alerts {
		out = append(out, GrowthTimelineEvent{
			At:     a.TriggeredAt,
			Kind:   "alert",
			Title:  "预警 · " + a.Kind,
			Detail: a.Detail,
		})
	}
	for _, t := range touches {
		out = append(out, GrowthTimelineEvent{
			At:     t.CreatedAt,
			Kind:   "touch",
			Title:  t.Method + " · " + t.OperatorName,
			Detail: t.Feedback,
		})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At > out[j-1].At; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// CreateGrowthTouch 追加一条触达记录。
func CreateGrowthTouch(t *GrowthTouch) error {
	t.CreatedAt = time.Now().Unix()
	return DB.Create(t).Error
}

// ListGrowthTouches 返回某个用户的触达记录，倒序。
func ListGrowthTouches(userId int, limit int) ([]GrowthTouch, error) {
	var rows []GrowthTouch
	if limit <= 0 {
		limit = 100
	}
	err := DB.Where("user_id = ?", userId).Order("created_at desc").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListRecentTouches 返回全局最近触达。
func ListRecentTouches(limit int) ([]GrowthTouch, error) {
	var rows []GrowthTouch
	if limit <= 0 {
		limit = 20
	}
	err := DB.Order("created_at desc").Limit(limit).Find(&rows).Error
	return rows, err
}

// CreateGrowthBatch 创建发放批次。
func CreateGrowthBatch(b *GrowthBatch) error {
	return DB.Create(b).Error
}

// ListGrowthBatches 返回批次列表。
func ListGrowthBatches(limit int) ([]GrowthBatch, error) {
	var rows []GrowthBatch
	if limit <= 0 {
		limit = 100
	}
	err := DB.Order("id desc").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListRunningBatches 返回观察窗内的批次。
func ListRunningBatches() ([]GrowthBatch, error) {
	var rows []GrowthBatch
	err := DB.Where("status = ?", "running").Find(&rows).Error
	return rows, err
}

// MarkBatchSettled 写入结算结果。
func MarkBatchSettled(id, incremental int, roi float64) error {
	return DB.Model(&GrowthBatch{}).Where("id = ?", id).
		Updates(map[string]any{
			"incremental_quota": incremental,
			"roi":               roi,
			"status":            "settled",
			"settled_at":        time.Now().Unix(),
		}).Error
}

// SumQuotaForUsers 汇总一组用户在窗口内的消耗，用于批次增量计算。
func SumQuotaForUsers(userIds []int, since int64) (int, error) {
	if len(userIds) == 0 || !DB.Migrator().HasTable("quota_data") {
		return 0, nil
	}
	var total int
	err := DB.Table("quota_data").
		Select("COALESCE(SUM(quota), 0)").
		Where("user_id IN ? AND created_at >= ?", userIds, since).
		Scan(&total).Error
	return total, err
}

// JoinUserIds 把 id 列表拼成逗号分隔串。
func JoinUserIds(ids []int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	return strings.Join(parts, ",")
}

// ParseUserIds 还原批次的目标名单。
func ParseUserIds(raw string) []int {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if id, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

/* APIDUO:growth:END */
