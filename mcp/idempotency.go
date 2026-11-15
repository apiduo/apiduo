package mcp

import (
	"encoding/json"
	"errors"
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
)

// IdempotencyRecord 是 generate 的幂等占位与结果。
type IdempotencyRecord struct {
	Id              int64  `json:"id" gorm:"primaryKey"`
	TokenId         int    `json:"token_id" gorm:"not null;uniqueIndex:idx_mcp_idem"`
	Tool            string `json:"tool" gorm:"type:varchar(32);not null;uniqueIndex:idx_mcp_idem"`
	ClientRequestId string `json:"client_request_id" gorm:"type:varchar(64);not null;uniqueIndex:idx_mcp_idem"`
	Status          string `json:"status" gorm:"type:varchar(16);not null"`
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
func reserveIdempotency(tokenId int, tool, clientRequestId string) (*idempotentResult, error) {
	now := common.GetTimestamp()
	rec := IdempotencyRecord{
		TokenId: tokenId, Tool: tool, ClientRequestId: clientRequestId,
		Status: idempotencyPending, CreatedAt: now, UpdatedAt: now,
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
	if existing.Status != idempotencyDone {
		if now-existing.CreatedAt > int64(idempotencyStaleAfter/time.Second) {
			// 首次调用已经死了：删掉占位重抢一次（最多递归一层）。
			model.DB.Delete(&IdempotencyRecord{}, existing.Id)
			return reserveIdempotency(tokenId, tool, clientRequestId)
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

// completeIdempotency 回填结果，让重试能拿到同一份响应。
//
// Upstream 是上游原始响应，重放用不到；不落，避免把几 MB 的 b64 塞进表。
// 超过上限时只标 done 不存内容 —— 重放会得到明确的「不可重放」而不是一次新生成。
func completeIdempotency(tokenId int, tool, clientRequestId, text string, out generateOutput) {
	out.Upstream = nil
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

func validateClientRequestID(id string) error {
	if n := len([]rune(id)); n == 0 || n > 64 {
		return userParamErrorf(CodeParamOutOfRange,
			"client_request_id 长度必须在 1–64 之间，收到 %d", n)
	}
	return nil
}
