package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// Settings 是 MCP 的可调参数。放在库里而不是编进二进制：
// 限流阈值、批量上限、超时这些值上线后一定要调，调一次发一个版本不值。
//
// 读取走内存快照（2 秒 TTL），写入立刻失效 —— 改完最多 2 秒生效，不用重启。
type Settings struct {
	// MaxBatchN 是「模型没声明 n 上限」时的兜底数量。
	MaxBatchN int `json:"max_batch_n"`
	// MaxPromptChars 是提示词的全局硬顶（按 rune）。
	MaxPromptChars int `json:"max_prompt_chars"`
	// ModelsListPerMin / TaskGetPerMin 是两个读工具的 per-token 窗口。
	ModelsListPerMin int `json:"models_list_per_min"`
	TaskGetPerMin    int `json:"task_get_per_min"`
	// MaxBodyBytes 是 /mcp 请求体上限。参考图可能以 data URL 内联，需要放宽。
	MaxBodyBytes int64 `json:"max_body_bytes"`
	// PollAfterMS 是返回给 client 的建议轮询间隔。
	PollAfterMS int `json:"poll_after_ms"`
	// TaskIDTTLHours 是签名句柄的有效期。
	TaskIDTTLHours int `json:"task_id_ttl_hours"`
	// 三个超时：生成、任务查询、模型清单。
	UpstreamTimeoutS int `json:"upstream_timeout_seconds"`
	TaskTimeoutS     int `json:"task_timeout_seconds"`
	ModelsTimeoutS   int `json:"models_timeout_seconds"`
	// FanoutParallel 是 n>1 拆单时的最大并发数。
	FanoutParallel int `json:"fanout_parallel"`
	// RecordTaskErrors 决定 task.get 失败时是否落观察样本。
	RecordTaskErrors bool `json:"record_task_errors"`
}

const settingsKey = "mcp_settings"

var defaultSettings = Settings{
	MaxBatchN:        maxBatchN,
	MaxPromptChars:   maxPromptChars,
	ModelsListPerMin: catalogListLimit,
	TaskGetPerMin:    taskGetLimit,
	MaxBodyBytes:     maxMCPBodyBytes,
	PollAfterMS:      pollAfterMS,
	TaskIDTTLHours:   int(taskIDTTL / time.Hour),
	UpstreamTimeoutS: 480,
	TaskTimeoutS:     30,
	ModelsTimeoutS:   15,
	FanoutParallel:   4,
	RecordTaskErrors: true,
}

var (
	settingsMu      sync.Mutex
	settingsCache   atomic.Pointer[Settings]
	settingsExpires atomic.Int64
)

const settingsTTL = 2 * time.Second

// LoadSettings 返回当前生效的参数。
//
// 与目录快照同样按 TTL 刷新：多副本下别的实例改了库，这边最多 2 秒跟上；
// 本实例写入时直接失效，不用等 TTL。
func LoadSettings() Settings {
	if s := settingsCache.Load(); s != nil && common.GetTimestamp() < settingsExpires.Load() {
		return *s
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if s := settingsCache.Load(); s != nil && common.GetTimestamp() < settingsExpires.Load() {
		return *s
	}
	s := readSettings()
	settingsCache.Store(&s)
	settingsExpires.Store(common.GetTimestamp() + int64(settingsTTL/time.Second))
	return s
}

func readSettings() Settings {
	out := defaultSettings
	if model.DB == nil {
		return out
	}
	var row Setting
	if err := model.DB.Where(map[string]any{"key": settingsKey}).First(&row).Error; err != nil {
		return out
	}
	if strings.TrimSpace(row.Value) != "" {
		// 未知键忽略、缺的键保留默认值：新版本加参数时，旧配置仍然可用。
		if err := json.Unmarshal([]byte(row.Value), &out); err != nil {
			common.SysError("[mcp] 设置解析失败，回落到默认值: " + err.Error())
			return defaultSettings
		}
	}
	return normalizeSettings(out)
}

// normalizeSettings 把越界值夹回合法区间。
//
// 不做「非法就整体拒绝」：一条配置写错就把整份设置打回默认，运维会找不到
// 自己刚改的那一项。夹住并继续，改动仍然生效。
func normalizeSettings(s Settings) Settings {
	s.MaxBatchN = clampInt(s.MaxBatchN, 1, 64, defaultSettings.MaxBatchN)
	s.MaxPromptChars = clampInt(s.MaxPromptChars, 100, 1_000_000, defaultSettings.MaxPromptChars)
	s.ModelsListPerMin = clampInt(s.ModelsListPerMin, 1, 100_000, defaultSettings.ModelsListPerMin)
	s.TaskGetPerMin = clampInt(s.TaskGetPerMin, 1, 100_000, defaultSettings.TaskGetPerMin)
	s.MaxBodyBytes = clampInt64(s.MaxBodyBytes, 64<<10, 128<<20, defaultSettings.MaxBodyBytes)
	s.PollAfterMS = clampInt(s.PollAfterMS, 500, 600_000, defaultSettings.PollAfterMS)
	s.TaskIDTTLHours = clampInt(s.TaskIDTTLHours, 1, 24*90, defaultSettings.TaskIDTTLHours)
	s.UpstreamTimeoutS = clampInt(s.UpstreamTimeoutS, 10, 1800, defaultSettings.UpstreamTimeoutS)
	s.TaskTimeoutS = clampInt(s.TaskTimeoutS, 3, 300, defaultSettings.TaskTimeoutS)
	s.ModelsTimeoutS = clampInt(s.ModelsTimeoutS, 3, 300, defaultSettings.ModelsTimeoutS)
	s.FanoutParallel = clampInt(s.FanoutParallel, 1, 16, defaultSettings.FanoutParallel)
	return s
}

func clampInt(v, lo, hi, def int) int {
	if v < lo || v > hi {
		if v == 0 {
			return def
		}
		// 只把越界的夹到边界，不回到默认值：0 通常表示「没填」，
		// 而 100000 这种明显越界的应该被夹成上限。
		if v < lo {
			return lo
		}
		return hi
	}
	return v
}

func clampInt64(v, lo, hi, def int64) int64 {
	if v < lo || v > hi {
		if v == 0 {
			return def
		}
		if v < lo {
			return lo
		}
		return hi
	}
	return v
}

// SaveSettings 合并写入并立刻失效缓存。
//
// patch 里出现什么改什么，没出现的保持当前值 —— 与 UpdateProfile 同样的理由：
// 漏传一个字段就把它改回默认，配置会被无声地改坏。
func SaveSettings(patch map[string]json.RawMessage) (Settings, error) {
	if len(patch) == 0 {
		return Settings{}, userErrf("请求体没有任何字段")
	}
	cur := LoadSettings()
	raw, err := json.Marshal(cur)
	if err != nil {
		return Settings{}, err
	}
	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &merged); err != nil {
		return Settings{}, err
	}
	known := map[string]bool{}
	for k := range merged {
		known[k] = true
	}
	for k, v := range patch {
		if !known[k] {
			return Settings{}, userErrf("未知的设置项：%s", k)
		}
		merged[k] = v
	}
	body, err := json.Marshal(merged)
	if err != nil {
		return Settings{}, err
	}
	var next Settings
	if err := json.Unmarshal(body, &next); err != nil {
		return Settings{}, userErrf("设置取值不合法：%s", err.Error())
	}
	next = normalizeSettings(next)

	stored, err := json.Marshal(next)
	if err != nil {
		return Settings{}, err
	}
	row := Setting{Key: settingsKey, Value: string(stored), UpdatedAt: common.GetTimestamp()}
	if err := model.DB.Where(map[string]any{"key": settingsKey}).
		Assign(map[string]any{"value": row.Value, "updated_at": row.UpdatedAt}).
		FirstOrCreate(&row).Error; err != nil {
		return Settings{}, err
	}
	ResetSettingsCache()
	return next, nil
}

// ResetSettingsCache 丢弃进程内快照。写入后调用；测试里也用它隔离。
func ResetSettingsCache() {
	settingsCache.Store(nil)
	settingsExpires.Store(0)
}

// Setting 是 mcp_settings 表的一行。
//
// 单行 JSON 而不是 key/value：参数之间有关联（比如 n 上限与拆单并发），
// 一次读出来才能保证同一次调用看到的是同一份配置。
type Setting struct {
	Key       string `json:"key" gorm:"primaryKey;size:64"`
	Value     string `json:"value" gorm:"type:text"`
	UpdatedAt int64  `json:"updated_at"`
}

func (Setting) TableName() string { return "mcp_settings" }

// clampDuration 把秒数转成 Duration，供调用点使用。
func secondsToDuration(s int) time.Duration { return time.Duration(s) * time.Second }

var errSettingsUnavailable = errors.New("设置不可用")
