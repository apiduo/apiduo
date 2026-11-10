package model

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// apiduo 扩展设置（「设置 → 扩展」五个模块）的写入期校验。
//
// 为什么要有这一层：前端对每个字段都做了约束，但接口层此前一条都没有。
// 实测直连 API 可以写出这些「前端造不出来」的配置 ——
//
//	artifact_storage_setting.enabled = true   而 targets 为空
//	artifact_storage_setting.targets = [{bad json
//	observability_setting.rules      = days:[9] / metadata_rate:999 / end<start
//	apiduo_appearance_setting.speed_ms = 999999
//	LoginTheme                       = neon-unknown
//
// 它们在运行时会静默降级（非法皮肤回落默认、坏规则永不命中、坏 targets 解析失败当空），
// 排查成本远高于在写入时拒掉。前端那几条规则原样搬到这里，文案保持能照着改。
//
// 另一个问题是「未知键」：批量接口此前对任何键名都回 200 并落库，改代码时手滑拼错
// 一个键名，界面和接口都显示保存成功，实际什么都没发生。见 OptionKeyWritable。

// apiduoOptionKeys 是扩展模块拥有写权限的键。
//
// 显式列出来而不是按前缀放行：「artifact_storage_setting.targts」这种拼错
// 落在前缀里，按前缀放行就永远发现不了。
var apiduoOptionKeys = map[string]struct{}{
	"LoginTheme": {},

	"apiduo_appearance_setting.enabled":      {},
	"apiduo_appearance_setting.style":        {},
	"apiduo_appearance_setting.color":        {},
	"apiduo_appearance_setting.speed_ms":     {},
	"apiduo_appearance_setting.ring_px":      {},
	"apiduo_appearance_setting.tail_deg":     {},
	"apiduo_appearance_setting.glow":         {},
	"apiduo_appearance_setting.holiday":      {},
	"apiduo_appearance_setting.holiday_from": {},
	"apiduo_appearance_setting.holiday_to":   {},

	"artifact_storage_setting.enabled":             {},
	"artifact_storage_setting.endpoint":            {},
	"artifact_storage_setting.bucket":              {},
	"artifact_storage_setting.region":              {},
	"artifact_storage_setting.access_key":          {},
	"artifact_storage_setting.secret_key":          {},
	"artifact_storage_setting.prefix":              {},
	"artifact_storage_setting.path_style":          {},
	"artifact_storage_setting.targets":             {},
	"artifact_storage_setting.presign_ttl_seconds": {},
	"artifact_storage_setting.max_object_bytes":    {},
	"artifact_storage_setting.rewrite_paths":       {},
	"artifact_storage_setting.field_names":         {},

	"observability_setting.parent_based_sampling": {},
	"observability_setting.metadata_sample_rate":  {},
	"observability_setting.content_sample_rate":   {},
	"observability_setting.rules":                 {},
	"observability_setting.timezone":              {},
	"observability_setting.trace_viewer_user_ids": {},
}

// apiduoAppearanceStyles 与前端的外观预设保持一致。
var apiduoAppearanceStyles = map[string]struct{}{
	"sheen": {}, "spotlight": {}, "pulse": {}, "flow": {}, "rainbow": {}, "none": {},
}

// apiduoLoginSkins 与 web/src/features/apiduo-login/skin.ts 的 LOGIN_SKIN_IDS 一致。
var apiduoLoginSkins = map[string]struct{}{
	"aurora": {}, "light": {}, "terminal": {}, "editorial": {}, "cyber": {}, "warm": {},
}

var (
	apiduoHexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	apiduoDateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	apiduoTimezone = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-/]*$`)
	apiduoClock    = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

// OptionKeyWritable 判断管理接口能否写这个键。
//
// 两来源：扩展模块自己的键（显式清单），以及 OptionMap 里已经存在的键
// （上游的所有设置都在里面，含从库里加载的历史键）。不在其中就拒 ——
// 拼错的键名必须报错，而不是回一句「保存成功」再把垃圾写进库里。
func OptionKeyWritable(key string) bool {
	if _, ok := apiduoOptionKeys[key]; ok {
		return true
	}
	common.OptionMapRWMutex.RLock()
	_, ok := common.OptionMap[key]
	common.OptionMapRWMutex.RUnlock()
	return ok
}

// ValidateApiduoOptionValue 做单键的形状与取值范围校验。非扩展键直接放行。
func ValidateApiduoOptionValue(key, value string) error {
	v := strings.TrimSpace(value)
	bad := func(format string, a ...any) error { return fmt.Errorf(format, a...) }

	switch key {
	case "LoginTheme":
		if _, ok := apiduoLoginSkins[v]; !ok {
			return bad("登录页风格只能是 aurora / light / terminal / editorial / cyber / warm")
		}

	case "apiduo_appearance_setting.enabled", "apiduo_appearance_setting.glow",
		"artifact_storage_setting.enabled", "artifact_storage_setting.path_style",
		"observability_setting.parent_based_sampling":
		if v != "true" && v != "false" {
			return bad("%s 只能是 true 或 false", key)
		}

	case "apiduo_appearance_setting.style":
		if _, ok := apiduoAppearanceStyles[v]; !ok {
			return bad("外观预设不存在：%q", v)
		}
	case "apiduo_appearance_setting.color":
		if !apiduoHexColor.MatchString(v) {
			return bad("高光颜色必须是 #RRGGBB")
		}
	case "apiduo_appearance_setting.speed_ms":
		return apiduoRange(key, v, 100, 60000)
	case "apiduo_appearance_setting.ring_px":
		return apiduoRange(key, v, 0.5, 8)
	case "apiduo_appearance_setting.tail_deg":
		return apiduoRange(key, v, 0, 360)
	case "apiduo_appearance_setting.holiday", "apiduo_appearance_setting.holiday_from",
		"apiduo_appearance_setting.holiday_to":
		if v != "" && !apiduoDateOnly.MatchString(v) {
			return bad("%s 需要 YYYY-MM-DD 或留空", key)
		}

	case "artifact_storage_setting.presign_ttl_seconds":
		return apiduoRange(key, v, 60, 86400)
	case "artifact_storage_setting.max_object_bytes":
		return apiduoRange(key, v, 0, 1<<40)
	case "artifact_storage_setting.endpoint":
		if v == "" {
			return nil
		}
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return bad("端点必须是 http(s) 地址，或留空")
		}
	case "artifact_storage_setting.targets":
		return apiduoValidateStorageTargets(v)
	case "artifact_storage_setting.rewrite_paths", "artifact_storage_setting.field_names":
		if v == "" || v == "null" {
			return nil
		}
		var anyVal any
		if err := json.Unmarshal([]byte(v), &anyVal); err != nil {
			return bad("%s 不是合法 JSON", key)
		}

	case "observability_setting.metadata_sample_rate", "observability_setting.content_sample_rate":
		return apiduoRange(key, v, 0, 100)
	case "observability_setting.rules":
		return apiduoValidateSampleRules(v)
	case "observability_setting.trace_viewer_user_ids":
		if v == "" || v == "null" || v == "[]" {
			return nil
		}
		var ids []int
		if err := json.Unmarshal([]byte(v), &ids); err != nil {
			return bad("Trace 可见范围必须是用户 ID 数组")
		}
	case "observability_setting.timezone":
		if v != "" && (!apiduoTimezone.MatchString(v) || len(v) > 64) {
			return bad("时区名不合法：%q", v)
		}
	}
	return nil
}

// ValidateApiduoOptionSet 做跨键校验，必须整批合并之后再看。
//
// 单独一条 enabled=true 是合法的，但它和 targets 合起来才决定「有没有可写的目标」。
// 逐键校验看不到同一批里另一个键的新值，所以只能在合并视图上判。
func ValidateApiduoOptionSet(incoming map[string]string) error {
	_, touchesEnabled := incoming["artifact_storage_setting.enabled"]
	_, touchesTargets := incoming["artifact_storage_setting.targets"]
	if !touchesEnabled && !touchesTargets {
		return nil
	}

	current := func(k string) string {
		common.OptionMapRWMutex.RLock()
		defer common.OptionMapRWMutex.RUnlock()
		return common.OptionMap[k]
	}
	enabled := current("artifact_storage_setting.enabled")
	if v, ok := incoming["artifact_storage_setting.enabled"]; ok {
		enabled = v
	}
	if !strings.EqualFold(strings.TrimSpace(enabled), "true") {
		return nil
	}
	targets := current("artifact_storage_setting.targets")
	if v, ok := incoming["artifact_storage_setting.targets"]; ok {
		targets = v
	}
	if apiduoCountStorageTargets(targets) == 0 {
		return fmt.Errorf("启用对象存储前，先添加至少一个带端点和存储桶的目标")
	}
	return nil
}

// apiduoRange 解析数字并检查区间，错误文案直接说清允许范围。
func apiduoRange(key, v string, min, max float64) error {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("%s 必须是数字", key)
	}
	if n < min || n > max {
		return fmt.Errorf("%s 超出范围：%g ~ %g（收到 %g）", key, min, max, n)
	}
	return nil
}

type apiduoStorageTarget struct {
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
}

func apiduoParseStorageTargets(raw string) ([]apiduoStorageTarget, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var out []apiduoStorageTarget
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("对象存储目标不是合法的 JSON 数组")
	}
	return out, nil
}

func apiduoCountStorageTargets(raw string) int {
	targets, err := apiduoParseStorageTargets(raw)
	if err != nil {
		return 0
	}
	n := 0
	for _, t := range targets {
		if strings.TrimSpace(t.Endpoint) != "" && strings.TrimSpace(t.Bucket) != "" {
			n++
		}
	}
	return n
}

// apiduoValidateStorageTargets 只校验「能被消费的形状」：JSON 数组、每项有端点和桶。
//
// 前端保存前会把这些过滤掉再写，所以线上数据里不该出现半截目标；
// 允许半截进来等于让「看起来配好了、实际没生效」重新变成可能。
func apiduoValidateStorageTargets(raw string) error {
	targets, err := apiduoParseStorageTargets(raw)
	if err != nil {
		return err
	}
	for i, t := range targets {
		if strings.TrimSpace(t.Endpoint) == "" || strings.TrimSpace(t.Bucket) == "" {
			return fmt.Errorf("第 %d 个对象存储目标缺端点或存储桶", i+1)
		}
	}
	return nil
}

type apiduoSampleRule struct {
	Days         []int   `json:"days"`
	Start        string  `json:"start"`
	End          string  `json:"end"`
	MetadataRate float64 `json:"metadata_rate"`
	ContentRate  float64 `json:"content_rate"`
}

// apiduoValidateSampleRules 补齐时间窗之外的字段校验。
//
// 时间窗本身由 observability_setting.ValidateRuleWindows 在更早的一层判，
// 这里再覆盖星期、采样率与「结束早于开始」—— 那三种在运行时都只会让规则永不命中，
// 面板上看不出是配置错了还是没人用。
func apiduoValidateSampleRules(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var rules []apiduoSampleRule
	if err := json.Unmarshal([]byte(trimmed), &rules); err != nil {
		return fmt.Errorf("采样规则不是合法 JSON")
	}
	for i, r := range rules {
		at := i + 1
		for _, d := range r.Days {
			if d < 1 || d > 7 {
				return fmt.Errorf("第 %d 条规则：星期只能是 1-7（收到 %d）", at, d)
			}
		}
		if r.MetadataRate < 0 || r.MetadataRate > 100 {
			return fmt.Errorf("第 %d 条规则：元数据采样率只能是 0-100（收到 %g）", at, r.MetadataRate)
		}
		if r.ContentRate < 0 || r.ContentRate > 100 {
			return fmt.Errorf("第 %d 条规则：内容采样率只能是 0-100（收到 %g）", at, r.ContentRate)
		}
		if r.Start != "" && r.End != "" {
			if !apiduoClock.MatchString(r.Start) || !apiduoClock.MatchString(r.End) {
				return fmt.Errorf("第 %d 条规则：时间要写成 HH:MM", at)
			}
			if r.End <= r.Start {
				return fmt.Errorf("第 %d 条规则：结束时间要晚于开始时间（%s → %s）", at, r.Start, r.End)
			}
		}
	}
	return nil
}
