// Package mcp 是 MCP 能力注册控制面与协议端点。
//
// 集成原则（与 new-api 上游解耦）：
//
//   - 本目录全部是新增文件，upstream 永远不会碰它，sync 时零冲突。
//   - 既有文件只有一处改动：router/main.go 里加一行 Register(router)，
//     并用 // APIDUO:mcp BEGIN / END 包裹，sync 时按标记重放即可。
//   - 建表不写进 model/main.go 的 AutoMigrate 列表，而是本包自己 Migrate()，
//     这样 model 包一行都不动。
//   - 表名统一 mcp_ 前缀，与既有表无任何关联。
//   - 读日志只查 model.LOG_DB，不写它；写日志的能力由 MCP 协议层负责。
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

// ModelProfile 是一个模型的能力声明，字段对齐 MCP 模型契约 v4：
// kind / execution / input / parameters 四层。
//
// 三态约定：指针字段为 nil 表示「未声明」，与 0 不是一回事。
// max_reference_images 缺失代表上限未知、本地不拦截；0 代表明确不支持垫图。
// 把 nil 当成 0 会锁死图生图模型，这是本项目最容易犯的错。
type ModelProfile struct {
	Id          int64  `json:"id" gorm:"primaryKey"`
	ModelId     string `json:"model_id" gorm:"size:191;not null;uniqueIndex:uk_mcp_profile_model"`
	DisplayName string `json:"display_name" gorm:"size:191"`
	Kind        string `json:"kind" gorm:"size:16;not null"`
	Protocol    string `json:"protocol" gorm:"size:32;not null"`
	Async       bool   `json:"async" gorm:"not null"`
	Enabled     bool   `json:"enabled" gorm:"not null;index"`

	MaxRefImages   *int `json:"max_ref_images"`
	MaxRefVideos   *int `json:"max_ref_videos"`
	MaxPromptChars *int `json:"max_prompt_chars"`

	// Parameters 是受控参数约束的 JSON：{"size":{"enum":["1K","2K"],"default":"2K"}}。
	// 用 text 而不是拆列：参数集合按模型而异，拆列会让每加一个参数就改一次表结构。
	Parameters string `json:"parameters" gorm:"type:text"`

	Summary   string `json:"summary" gorm:"size:512"`
	Tags      string `json:"tags" gorm:"size:255"`
	Source    string `json:"source" gorm:"size:16"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func (ModelProfile) TableName() string { return "mcp_model_profiles" }

// ErrorRule 是错误到「agent 该不该重试」的映射。
//
// Scope=global 的规则对所有模型生效；Scope=model 只覆盖指定模型的例外。
// 不要在模型级重复全局内容 —— 重复会让「全局改了但模型级没改」变成隐性 bug。
type ErrorRule struct {
	Id           int64  `json:"id" gorm:"primaryKey"`
	Scope        string `json:"scope" gorm:"size:16;not null;index"`
	ModelId      string `json:"model_id" gorm:"size:191;index"`
	Match        string `json:"match" gorm:"size:128;not null"`
	Retryable    bool   `json:"retryable" gorm:"not null"`
	RetryAction  string `json:"retry_action" gorm:"size:24;not null"`
	RetryAfterMs int    `json:"retry_after_ms"`
	Hint         string `json:"hint" gorm:"type:text"`
	Enabled      bool   `json:"enabled" gorm:"not null"`
	Sort         int    `json:"sort"`
}

func (ErrorRule) TableName() string { return "mcp_error_rules" }

// ErrProfileNotFound 表示模型声明不存在。
var ErrProfileNotFound = errors.New("模型声明不存在")

// ErrProfileDuplicate 表示这个模型已经有能力声明了。
//
// 单独一个哨兵是为了让接口层能回一句人话：把 gorm 的
// `Error 1062 (23000): Duplicate entry 'x' for key 'mcp_model_profiles.uk_...'`
// 原样回给前端，等于把表结构和索引名贴到面板上，管理员还看不懂要做什么。
var ErrProfileDuplicate = errors.New("该模型已有能力声明，直接编辑它即可，不要重复新建")

// userErr 标记「可以原样展示给用户」的错误。
//
// 与数据库/内部错误的区别必须显式标出来：错误文案会出现在面板红条里，
// 只有本包自己写的中文提示能出去，驱动返回的原始报文一律留日志。
type userErr string

func (e userErr) Error() string { return string(e) }

func userErrf(format string, a ...any) error { return userErr(fmt.Sprintf(format, a...)) }

// isDuplicateKeyErr 只兜「并发插入撞唯一索引」这一种情况。
//
// 主库要同时支持 MySQL / PostgreSQL / SQLite，而 gorm.Config 没开 TranslateError，
// 所以按三家各自会写进错误里的固定片段识别。认不出来就走通用错误分支，
// 那条分支不会把原文回给前端，所以漏识别最多是文案不够准，不会泄露。
func isDuplicateKeyErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate entry") || // MySQL 1062
		strings.Contains(s, "unique constraint failed") || // SQLite
		strings.Contains(s, "duplicate key value") // PostgreSQL 23505
}

// Migrate 建表并写入全局默认错误规则。
//
// 由本包自己调用，不进 model/main.go —— 那是既有文件，改一处就多一个冲突点。
func Migrate() error {
	if model.DB == nil {
		return errors.New("主库未初始化")
	}
	if err := model.DB.AutoMigrate(&ModelProfile{}, &ErrorRule{}); err != nil {
		return err
	}
	return seedGlobalErrorRules()
}

// globalErrorRules 是全局默认规则。
//
// 默认值刻意保守：未匹配到的错误在调用侧一律按不可重试处理，
// 因为重试的代价是计费，而多问一轮的代价只是对话轮次。
var globalErrorRules = []ErrorRule{
	{Match: "400:param", Retryable: true, RetryAction: "change_param", Sort: 10,
		Hint: "参数超出该模型范围，按 valid_values 改后重试"},
	{Match: "400:content_policy", Retryable: true, RetryAction: "change_prompt", Sort: 20,
		Hint: "提示词被内容策略拒绝，原样重发必然再失败"},
	{Match: "401", Retryable: false, RetryAction: "none", Sort: 30,
		Hint: "key 无效或过期，重试只会一直失败"},
	{Match: "402", Retryable: false, RetryAction: "none", Sort: 40,
		Hint: "额度不足，需要充值，重试无用"},
	{Match: "429", Retryable: true, RetryAction: "wait", RetryAfterMs: 3000, Sort: 50,
		Hint: "被限流，参数不用改，退避后再发"},
	{Match: "5xx", Retryable: true, RetryAction: "wait", RetryAfterMs: 5000, Sort: 60,
		Hint: "上游故障，指数退避重试，最多 2 次"},
	{Match: "task:FAILED", Retryable: true, RetryAction: "change_param", Sort: 70,
		Hint: "任务失败，同参数重提会再失败，先看 failure_reason"},
}

func seedGlobalErrorRules() error {
	var count int64
	if err := model.DB.Model(&ErrorRule{}).Where("scope = ?", "global").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	rows := make([]ErrorRule, 0, len(globalErrorRules))
	for _, r := range globalErrorRules {
		r.Scope = "global"
		r.Enabled = true
		rows = append(rows, r)
	}
	return model.DB.Create(&rows).Error
}

// ListProfiles 返回全部模型声明，按 kind、model_id 排序。
func ListProfiles() ([]ModelProfile, error) {
	out := make([]ModelProfile, 0)
	err := model.DB.Order("kind asc, model_id asc").Find(&out).Error
	return out, err
}

// GetProfile 按 id 取一条。
func GetProfile(id int64) (ModelProfile, error) {
	var p ModelProfile
	err := model.DB.First(&p, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ModelProfile{}, ErrProfileNotFound
	}
	return p, err
}

// GetProfileByModelId 按模型 ID 取一条，供 MCP 协议层合并能力时使用。
func GetProfileByModelId(modelId string) (ModelProfile, bool) {
	var p ModelProfile
	err := model.DB.Where("model_id = ?", strings.TrimSpace(modelId)).First(&p).Error
	if err != nil {
		return ModelProfile{}, false
	}
	return p, true
}

// createProfile 建一条新声明，并把「这个模型已经有声明」变成一句能照着做的话。
//
// 先查一次是为了稳定文案（唯一索引报错在三种库上文案都不一样），
// 但并发下仍可能两个请求同时通过检查，所以 Create 失败还要再认一次重复键。
func createProfile(p *ModelProfile) error {
	var exists ModelProfile
	err := model.DB.Select("id").Where("model_id = ?", p.ModelId).First(&exists).Error
	if err == nil {
		return ErrProfileDuplicate
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := model.DB.Create(p).Error; err != nil {
		if isDuplicateKeyErr(err) {
			return ErrProfileDuplicate
		}
		return err
	}
	return nil
}

// SaveProfile 新建或更新。更新时不允许改 ModelId —— 它是与渠道对齐的业务键，
// 改名等于换一个模型，应该新建一条而不是原地改。
func SaveProfile(p *ModelProfile) error {
	p.ModelId = strings.TrimSpace(p.ModelId)
	if p.ModelId == "" {
		return userErrf("模型 ID 不能为空")
	}
	if p.Kind != "image" && p.Kind != "video" && p.Kind != "search" {
		return userErrf("产物类型不合法: %q", p.Kind)
	}
	if p.Protocol == "" {
		p.Protocol = defaultProtocol(p.Kind)
	}
	if p.Source == "" {
		p.Source = "manual"
	}
	if p.Parameters != "" && !json.Valid([]byte(p.Parameters)) {
		return userErrf("参数约束不是合法 JSON")
	}
	now := common.GetTimestamp()
	p.UpdatedAt = now

	if p.Id == 0 {
		p.CreatedAt = now
		return createProfile(p)
	}
	var existing ModelProfile
	if err := model.DB.First(&existing, p.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrProfileNotFound
		}
		return err
	}
	// 回填被忽略的字段：调用方传了新的 model_id 时，返回体里必须还是库里那份，
	// 否则前端拿到的对象与下次读库的结果不一致，看起来像「改名成功了」。
	p.ModelId = existing.ModelId
	p.CreatedAt = existing.CreatedAt
	// 启停是独立动作，不走「保存声明」这条路。
	//
	// 不只是洁癖：bool 的零值是 false，任何漏传 enabled 的客户端（比如某个
	// 只改显示名的脚本）都会在保存时把模型静默停用 —— agent 那边直接看不到它，
	// 而界面显示的是「保存成功」。
	p.Enabled = existing.Enabled
	return model.DB.Model(&ModelProfile{}).Where("id = ?", p.Id).Select(
		"display_name", "kind", "protocol", "async",
		"max_ref_images", "max_ref_videos", "max_prompt_chars",
		"parameters", "summary", "tags", "source", "updated_at",
	).Updates(p).Error
}

// profilePatchKeys 是「保存声明」允许改的字段。
//
// 用键集合而不是整份结构体：只有请求真的给出某个键，才覆盖它。
// 这是唯一能同时表达「显式 false」和「没传」的做法 —— Go 的 bool 零值做不到，
// 而这两者在这里完全不同：漏传 async 会把异步模型改成同步，漏传 enabled 会把它停用，
// 两次界面都显示「保存成功」。
//
// model_id 不在集合里（改名等于换模型）；enabled 也不在（启停走 SetProfileEnabled）。
var profilePatchKeys = []string{
	"display_name", "kind", "protocol", "async",
	"max_ref_images", "max_ref_videos", "max_prompt_chars",
	"parameters", "summary", "tags", "source",
}

// UpdateProfile 以库里的行做底稿，只覆盖请求里出现的字段。
func UpdateProfile(id int64, patch map[string]json.RawMessage) (ModelProfile, error) {
	if len(patch) == 0 {
		return ModelProfile{}, userErrf("请求体没有任何字段")
	}
	merged, err := GetProfile(id)
	if err != nil {
		return ModelProfile{}, err
	}
	var incoming ModelProfile
	raw, err := json.Marshal(patch)
	if err != nil {
		return ModelProfile{}, err
	}
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return ModelProfile{}, userErrf("请求体不是合法 JSON: %v", err)
	}
	for _, key := range profilePatchKeys {
		if _, given := patch[key]; !given {
			continue
		}
		switch key {
		case "display_name":
			merged.DisplayName = incoming.DisplayName
		case "kind":
			merged.Kind = incoming.Kind
		case "protocol":
			merged.Protocol = incoming.Protocol
		case "async":
			merged.Async = incoming.Async
		case "max_ref_images":
			merged.MaxRefImages = incoming.MaxRefImages
		case "max_ref_videos":
			merged.MaxRefVideos = incoming.MaxRefVideos
		case "max_prompt_chars":
			merged.MaxPromptChars = incoming.MaxPromptChars
		case "parameters":
			merged.Parameters = incoming.Parameters
		case "summary":
			merged.Summary = incoming.Summary
		case "tags":
			merged.Tags = incoming.Tags
		case "source":
			merged.Source = incoming.Source
		}
	}
	if err := SaveProfile(&merged); err != nil {
		return ModelProfile{}, err
	}
	// 回读一次：SaveProfile 会回填 model_id / created_at / enabled，
	// 那些改动在值传递里拿不到，而调用方要的正是库里的真值。
	return GetProfile(id)
}

func defaultProtocol(kind string) string {
	if kind == "video" {
		return "openai_video"
	}
	return "openai_image"
}

// SetProfileEnabled 单独翻转启停。
//
// 与 SaveProfile 分开：保存声明时 enabled 一律保持原值，启停只走这里，
// 这样两条路径的语义都是确定的，不存在「保存一次顺手改了状态」。
func SetProfileEnabled(id int64, enabled bool) error {
	res := model.DB.Model(&ModelProfile{}).Where("id = ?", id).Updates(map[string]any{
		"enabled":    enabled,
		"updated_at": common.GetTimestamp(),
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrProfileNotFound
	}
	return nil
}

// DeleteProfile 删除一条声明。
func DeleteProfile(id int64) error {
	return model.DB.Delete(&ModelProfile{}, id).Error
}

// ListErrorRules 返回全局规则 + 指定模型的覆盖规则。modelId 为空时只返回全局。
func ListErrorRules(modelId string) ([]ErrorRule, error) {
	out := make([]ErrorRule, 0)
	q := model.DB.Where("scope = ?", "global")
	if m := strings.TrimSpace(modelId); m != "" {
		q = model.DB.Where("scope = ? OR (scope = ? AND model_id = ?)", "global", "model", m)
	}
	err := q.Order("scope asc, sort asc, id asc").Find(&out).Error
	return out, err
}

// SaveErrorRule 更新一条规则。全局规则不允许改 Match（它是识别依据）。
func SaveErrorRule(r *ErrorRule) error {
	if r.Id == 0 {
		return userErrf("规则不存在")
	}
	var existing ErrorRule
	if err := model.DB.First(&existing, r.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return userErrf("规则不存在")
		}
		return err
	}
	if existing.Scope == "global" {
		r.Match = existing.Match
		r.Scope = "global"
		r.ModelId = ""
	} else {
		r.Scope = "model"
		r.ModelId = existing.ModelId
	}
	return model.DB.Model(&ErrorRule{}).Where("id = ?", r.Id).Select(
		"match", "retryable", "retry_action", "retry_after_ms", "hint", "enabled", "sort",
	).Updates(r).Error
}

// errorRulePatchKeys 是「更新规则」允许改的字段。
//
// match 单独处理：模型级规则可以改匹配串，全局规则的 match 是识别依据，改了就不是同一条规则。
var errorRulePatchKeys = []string{
	"retryable", "retry_action", "retry_after_ms", "hint", "enabled", "sort",
}

// UpdateErrorRule 以库里的行为底稿，只覆盖请求里出现的字段。
//
// 同理于 UpdateProfile：漏传 retryable 会把一条可重试规则改成不可重试，
// 而 agent 拿到的是「不要重试」——比多试一次贵得多。
func UpdateErrorRule(id int64, patch map[string]json.RawMessage) (ErrorRule, error) {
	if len(patch) == 0 {
		return ErrorRule{}, userErrf("请求体没有任何字段")
	}
	merged, err := GetErrorRule(id)
	if err != nil {
		return ErrorRule{}, err
	}
	var incoming ErrorRule
	raw, err := json.Marshal(patch)
	if err != nil {
		return ErrorRule{}, err
	}
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return ErrorRule{}, userErrf("请求体不是合法 JSON: %v", err)
	}
	for _, key := range errorRulePatchKeys {
		if _, given := patch[key]; !given {
			continue
		}
		switch key {
		case "retryable":
			merged.Retryable = incoming.Retryable
		case "retry_action":
			merged.RetryAction = incoming.RetryAction
		case "retry_after_ms":
			merged.RetryAfterMs = incoming.RetryAfterMs
		case "hint":
			merged.Hint = incoming.Hint
		case "enabled":
			merged.Enabled = incoming.Enabled
		case "sort":
			merged.Sort = incoming.Sort
		}
	}
	if _, given := patch["match"]; given && merged.Scope != "global" {
		merged.Match = incoming.Match
	}
	if err := SaveErrorRule(&merged); err != nil {
		return ErrorRule{}, err
	}
	return merged, nil
}

// GetErrorRule 按 id 取一条规则。
func GetErrorRule(id int64) (ErrorRule, error) {
	var r ErrorRule
	if err := model.DB.First(&r, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrorRule{}, userErrf("规则不存在")
		}
		return ErrorRule{}, err
	}
	return r, nil
}

// ParamNames 返回需要观察的参数名：内置常见参数 + 该模型声明的参数。
//
// 内置四个是跨厂商通用的（尺寸/时长/数量/分辨率）；声明的参数覆盖新出现的名字。
// 取并集是为了不漏，多查一个空参数只多一次毫秒级查询。
func ParamNames(p ModelProfile) []string {
	set := map[string]bool{"size": true, "duration": true, "n": true, "resolution": true}
	// 参考图张数也在观察范围内：max_ref_images 是声明过的上限，
	// 没有对照就只能靠猜它合不合适。快照里本来就写了这个键
	// （snapshotParams 会补 reference_images），此前却没人统计。
	if p.MaxRefImages != nil {
		set["reference_images"] = true
	}
	if p.Parameters != "" {
		var declared map[string]any
		if err := json.Unmarshal([]byte(p.Parameters), &declared); err == nil {
			for k := range declared {
				set[k] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
