package mcp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

// taskID 是自包含签名的任务标识。
//
// 为什么不存表：MCP 是无状态多副本的，任何副本收到 task.get 都要能处理。
// 把 model 与上游任务键编进 ID，外面套一层 HMAC 防伪造，就不需要共享存储。
//
// 密钥为什么不再是 api_key：那会让「用户重置令牌」等于「在途任务全部作废」。
// 改用系统密钥（options 表，跨副本共享），签名表达的是归属而不是凭证绑定；
// 归属由 payload 里的 UserId 承担，越权查询仍被挡住。
type taskPayload struct {
	Model    string `json:"m"`
	Upstream string `json:"u"`
	Kind     string `json:"k"`
	// UserId 是签发时的调用方。0 只出现在测试里（无鉴权的 httptest 环境）。
	UserId   int   `json:"uid,omitempty"`
	IssuedAt int64 `json:"t"`
	// ExpireAt 让句柄有寿命：签名只证明「是我们发的」，不证明「永远有效」。
	ExpireAt int64 `json:"e,omitempty"`
}

var (
	errTaskIDMalformed = errors.New("task_id 不合法")
	errTaskIDExpired   = errors.New("task_id 已过期")
	errTaskIDForeign   = errors.New("task_id 不属于当前用户")
)

const (
	// taskSigningKeyOption 是系统签名密钥在 options 表里的键。
	taskSigningKeyOption = "mcp_task_signing_key"
	// taskIDTTL 是句柄有效期。视频任务分钟级完成，7 天足够覆盖「过一天再来取」。
	taskIDTTL = 7 * 24 * time.Hour
)

var (
	taskKeyMu sync.Mutex
	taskKey   []byte
)

// taskSigningKey 返回跨副本一致的签名密钥。
//
// 必须在库里而不是进程内随机：A 副本签发的 task_id 要能被 B 副本解开，
// 否则水平扩容直接退化成「必须粘性路由」。
//
// 这里刻意不用 sync.Once：它会把「第一次读库失败」永久缓存成错误，
// 一次数据库抖动就等于 task.get 在本进程内彻底不可用，直到重启。
func taskSigningKey() ([]byte, error) {
	taskKeyMu.Lock()
	defer taskKeyMu.Unlock()
	if taskKey != nil {
		return taskKey, nil
	}
	key, err := loadOrCreateTaskSigningKey()
	if err != nil {
		return nil, err
	}
	taskKey = key
	return key, nil
}

// resetTaskSigningKey 丢掉进程内缓存。测试模拟「换副本」时用。
func resetTaskSigningKey() {
	taskKeyMu.Lock()
	taskKey = nil
	taskKeyMu.Unlock()
}

// loadOrCreateTaskSigningKey 读库里的密钥，没有就生成一次。
//
// 并发首启时两个副本都可能走到 Create：主键冲突的那一方回读库里的值，
// 保证最终只有一个密钥生效（而不是两台各自认为自己是对的）。
func loadOrCreateTaskSigningKey() ([]byte, error) {
	if model.DB == nil {
		return nil, errors.New("主库未初始化")
	}
	read := func() ([]byte, error) {
		var opt model.Option
		if err := optionQuery(taskSigningKeyOption).First(&opt).Error; err != nil {
			return nil, err
		}
		raw, err := hex.DecodeString(strings.TrimSpace(opt.Value))
		if err != nil {
			return nil, err
		}
		if len(raw) < 32 {
			return nil, errors.New("mcp_task_signing_key 长度不足 32 字节")
		}
		return raw[:32], nil
	}

	key, err := read()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if err := model.DB.Create(&model.Option{
		Key: taskSigningKeyOption, Value: hex.EncodeToString(raw),
	}).Error; err != nil {
		// 另一个副本先写成功：以库里的为准。
		if key, readErr := read(); readErr == nil {
			return key, nil
		}
		return nil, err
	}
	return raw, nil
}

// optionQuery 按 key 查 options 表。
//
// 用 map 条件而不是裸 "key = ?"：key 是 MySQL 保留字，裸写会报 1064
// （线上 0.0.142 首启就是这么炸的，SQLite 测不出来）。GORM 对 map 条件
// 按方言加引号，三种库都能跑。
func optionQuery(key string) *gorm.DB {
	return model.DB.Where(map[string]any{"key": key})
}

func signTaskID(p taskPayload) (string, error) {
	key, err := taskSigningKey()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	p.IssuedAt = now
	if p.ExpireAt == 0 {
		p.ExpireAt = now + int64(secondsToDuration(LoadSettings().TaskIDTTLHours*3600)/time.Second)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// parseTaskID 验签并校验归属与有效期。
//
// userId 是调用方当前身份；0 表示「不校验归属」，只给无鉴权的测试环境用。
// 生产路径一定 > 0，否则同一部署里任何人拿到 task_id 都能读别人的任务。
func parseTaskID(token string, userId int) (taskPayload, error) {
	key, err := taskSigningKey()
	if err != nil {
		return taskPayload{}, err
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return taskPayload{}, errTaskIDMalformed
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0]))
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return taskPayload{}, errTaskIDMalformed
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return taskPayload{}, errTaskIDMalformed
	}
	var p taskPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return taskPayload{}, errTaskIDMalformed
	}
	if p.Model == "" || p.Upstream == "" {
		return taskPayload{}, errTaskIDMalformed
	}
	if p.Kind == "" {
		p.Kind = "image"
	}
	if p.ExpireAt > 0 && time.Now().Unix() > p.ExpireAt {
		return taskPayload{}, errTaskIDExpired
	}
	if p.UserId != 0 && userId != p.UserId {
		return taskPayload{}, errTaskIDForeign
	}
	return p, nil
}
