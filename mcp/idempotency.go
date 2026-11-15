package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm/clause"
)

// 幂等表：一次生成调用对应一行，(token_id, tool, client_request_id) 唯一。
//
// 为什么不用进程内 LRU：网关是多副本的，重试很可能落到另一台；DB 唯一索引
// 是唯一跨副本生效的判重。也不是查 logs 表 —— 那边是异步批量写的账本，
// 重试那一刻行还没落库（可见性对不上），而且受日志保留期约束。
const (
	idempotencyPending  = "pending"
	idempotencyDone     = "done"
	maxIdempotentResult = 256 << 10
	idempotencyTTL      = 24 * time.Hour
	// idempotencyStaleAfter 之后仍停在 pending，视为「首次调用进程已死」。
	//
	// 没有这条，一次崩溃就会把这个键锁死到 24 小时清理 —— 调用方既不能重放
	// （没结果），也不能重试（一直 in-flight）。
	idempotencyStaleAfter = 10 * time.Minute
)

var (
	errIdempotencyInFlight = errors.New("同一 client_request_id 的请求仍在处理中")
	errReplayUnavailable   = errors.New("首次结果未缓存，无法重放")
	// errIdempotencyKeyReused：键相同但参数不同。既不能重放（结果对不上），
	// 也不能当成新请求发（那正好是幂等要防的重复计费）。
	errIdempotencyKeyReused = errors.New("同一 client_request_id 已用于另一组参数")
)

// IdempotencyRecord 是 generate 的幂等占位与结果。
type IdempotencyRecord struct {
	Id              int64  `json:"id" gorm:"primaryKey"`
	TokenId         int    `json:"token_id" gorm:"not null;uniqueIndex:idx_mcp_idem"`
	Tool            string `json:"tool" gorm:"type:varchar(32);not null;uniqueIndex:idx_mcp_idem"`
	ClientRequestId string `json:"client_request_id" gorm:"type:varchar(64);not null;uniqueIndex:idx_mcp_idem"`
	Status          string `json:"status" gorm:"type:varchar(16);not null"`
	// RequestHash 是首次请求的指纹（工具 + 归一化后的载荷）。
	//
	// 没有它，同一个键配另一组参数会静默拿回上一次的产物：agent 以为生成了
	// 一辆红色跑车，实际拿到的是十分钟前的苹果 —— 比报错危险得多。
	// 空值表示本次上线前写入的旧行，按「无法判定」放行，不做回溯校验。
	RequestHash string `json:"request_hash" gorm:"type:varchar(64)"`
	// Result 是 idempotentResult 的 JSON；空表示「首次成功但结果太大，不缓存」。
	Result    string `json:"result" gorm:"type:text"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;index"`
	UpdatedAt int64  `json:"updated_at" gorm:"bigint"`
}

func (IdempotencyRecord) TableName() string { return "mcp_idempotency" }

// idempotentResult 是重放所需的全部内容。
//
// 存 text 而不是重算：文本取决于当时的模型默认值、产物数量与内联 b64 提示，
// 重算迟早与首次不一致。
type idempotentResult struct {
	Text   string         `json:"text"`
	Output generateOutput `json:"output"`
}

// reserveIdempotency 占位或返回已有结果。
//
// 返回 (nil, nil) 表示这是首次调用，调用方继续执行；返回非 nil 表示命中，
// 直接重放；errIdempotencyInFlight 表示同一键的首次调用还没结束。
func reserveIdempotency(tokenId int, tool, clientRequestId, requestHash string) (*idempotentResult, error) {
	now := common.GetTimestamp()
	rec := IdempotencyRecord{
		TokenId: tokenId, Tool: tool, ClientRequestId: clientRequestId,
		Status: idempotencyPending, RequestHash: requestHash,
		CreatedAt: now, UpdatedAt: now,
	}
	res := model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected > 0 {
		maybeSweepIdempotency(now)
		return nil, nil
	}

	var existing IdempotencyRecord
	if err := model.DB.Where(
		"token_id = ? AND tool = ? AND client_request_id = ?",
		tokenId, tool, clientRequestId,
	).First(&existing).Error; err != nil {
		return nil, err
	}
	// 先判指纹再判状态：键相同、参数不同的请求，无论首次是 pending 还是 done，
	// 都不该被当成「同一请求」—— 前者回 in-flight 会引导调用方等待一个
	// 与它无关的结果，后者更糟，直接给一张别的图。
	if existing.RequestHash != "" && requestHash != "" && existing.RequestHash != requestHash {
		return nil, errIdempotencyKeyReused
	}
	if existing.Status != idempotencyDone {
		if now-existing.CreatedAt > int64(idempotencyStaleAfter/time.Second) {
			// 首次调用已经死了：删掉占位重抢一次（最多递归一层）。
			model.DB.Delete(&IdempotencyRecord{}, existing.Id)
			return reserveIdempotency(tokenId, tool, clientRequestId, requestHash)
		}
		return nil, errIdempotencyInFlight
	}
	if existing.Result == "" {
		return nil, errReplayUnavailable
	}
	var out idempotentResult
	if err := json.Unmarshal([]byte(existing.Result), &out); err != nil {
		return nil, errReplayUnavailable
	}
	return &out, nil
}

// requestFingerprint 把「工具 + 归一化载荷」压成一个稳定的哈希。
//
// 用 payload 而不是原始入参：buildPayload 已经把默认值补齐、把比例/别名归一，
// 所以「第一次不传 duration」和「第二次显式传 duration=5」得到同一个指纹，
// 不会把等价的两次请求误判成参数不同。encoding/json 对 map 按键排序，结果稳定。
func requestFingerprint(kind string, payload map[string]any) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(kind+"\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

// completeIdempotency 回填结果，让重试能拿到同一份响应。
//
// Upstream 是上游原始响应，重放用不到；不落，避免把几 MB 的 b64 塞进表。
// 超过上限时只标 done 不存内容 —— 重放会得到明确的「不可重放」而不是一次新生成。
func completeIdempotency(tokenId int, tool, clientRequestId, text string, out generateOutput) {
	// 只落可达地址，不落图片字节。
	//
	// 有些渠道会忽略 response_format=url 照旧回 b64 —— 那是几 MB 的图片被塞进
	// MySQL，重放时还要再塞回上下文。URL 能表达同样的东西，且后台本来就存着。
	// 全部产物都是 b64 时，如实告诉调用方「结果没缓存」，而不是给一个空数组。
	inline := 0
	// 必须新开切片：out.Outputs 与调用方要返回给用户的是同一个底层数组，
	// 原地裁剪会把「本次响应」里的 b64 也一起清掉 —— 调用方明明该拿到图，
	// 却只剩一个空产物。
	kept := make([]outputItem, 0, len(out.Outputs))
	for _, o := range out.Outputs {
		if o.B64JSON == "" {
			kept = append(kept, o)
			continue
		}
		inline++
		if o.URL == "" {
			continue
		}
		o.B64JSON = ""
		kept = append(kept, o)
	}
	out.Outputs = kept
	if inline > 0 {
		text += fmt.Sprintf("（其中 %d 个以 base64 内联返回，未缓存；重放只给地址）", inline)
	}
	raw, err := json.Marshal(idempotentResult{Text: text, Output: out})
	if err != nil || len(raw) > maxIdempotentResult {
		raw = nil
	}
	model.DB.Model(&IdempotencyRecord{}).
		Where("token_id = ? AND tool = ? AND client_request_id = ?", tokenId, tool, clientRequestId).
		Updates(map[string]any{
			"status":     idempotencyDone,
			"result":     string(raw),
			"updated_at": common.GetTimestamp(),
		})
}

// releaseIdempotency 在**未产生计费**的失败后释放占位，让调用方可以重试。
//
// 只由占位成功的那一次调用执行：并发重试拿到的是 in-flight，不会来删。
func releaseIdempotency(tokenId int, tool, clientRequestId string) {
	model.DB.Where(
		"token_id = ? AND tool = ? AND client_request_id = ?",
		tokenId, tool, clientRequestId,
	).Delete(&IdempotencyRecord{})
}

var idempotencySweptAt atomic.Int64

// maybeSweepIdempotency 每小时清一次过期行。
//
// 同步做，但一小时只可能命中一次（CAS 过的），代价可以忽略；
// 开 goroutine 反而会和测试替换全局 DB 打架。
func maybeSweepIdempotency(now int64) {
	last := idempotencySweptAt.Load()
	if now-last < 3600 || !idempotencySweptAt.CompareAndSwap(last, now) {
		return
	}
	model.DB.Where("created_at < ?", now-int64(idempotencyTTL/time.Second)).
		Delete(&IdempotencyRecord{})
}

// replayText 在重放结果前加一句说明，避免 agent 以为真的又生成了一次。
func replayText(r *idempotentResult) string {
	return "（同一 client_request_id 的重复调用，复用首次结果，未重复生成）\n" + r.Text
}

// replayRefreshedText 用于「重放时又查了一次任务状态」的情形：
// 复用是首次提交的任务，但状态是刚刚取的，不能说成「复用首次结果」。
func replayRefreshedText(text string) string {
	return "（同一 client_request_id 的重复调用，复用首次提交的任务，未重复生成）\n" + text
}

func validateClientRequestID(id string) error {
	if n := len([]rune(id)); n == 0 || n > 64 {
		return userParamErrorf(CodeParamOutOfRange,
			"client_request_id 长度必须在 1–64 之间，收到 %d", n)
	}
	return nil
}
