package model

import (
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// ChannelCost 是某个渠道的采购单价。
//
// 为什么按渠道而不是按模型：同一个模型可以从多个渠道进货，价格完全不同
// （官方直连 vs 中转）。运营真正要回答的是「哪个渠道在亏钱」。
type ChannelCost struct {
	Id        int `json:"id" gorm:"primaryKey"`
	ChannelId int `json:"channel_id" gorm:"uniqueIndex;default:0"`
	// 单价单位统一用「美元 / 百万 token」，与上游报价习惯一致，
	// 避免运营在录入时自己做换算。
	InputPerMillion  float64 `json:"input_per_million" gorm:"default:0"`
	OutputPerMillion float64 `json:"output_per_million" gorm:"default:0"`
	Note             string  `json:"note" gorm:"size:255;default:''"`
	UpdatedAt        int64   `json:"updated_at" gorm:"default:0"`
}

// CostResult 是一次成本核算的结果，金额均为美元。
type CostResult struct {
	RevenueUSD float64 `json:"revenue_usd"`
	CostUSD    float64 `json:"cost_usd"`
	// MarginUSD 为负表示倒挂：卖出去的钱不够付上游的钱。
	MarginUSD float64 `json:"margin_usd"`
	// MarginPct 在收入为 0 时无意义，置 0 而不是除零。
	MarginPct float64 `json:"margin_pct"`
}

// ComputeCost 按单价与用量算一次成本。
//
// tokens 与 quota 都来自同一条日志，所以这是「这一笔请求赚了多少」。
// quota 是内部计费单位，除以 QuotaPerUnit 换算成美元；
// 单价是「每百万 token」，所以 token 数要先除以一百万。
func ComputeCost(inputTokens, outputTokens, quota int, cost *ChannelCost) CostResult {
	revenue := 0.0
	// 直接读 common 的计费单位，不复制一份：两份数字迟早会不一致，
	// 而这类不一致不会报错，只会让毛利算错。
	if common.QuotaPerUnit > 0 {
		revenue = float64(quota) / common.QuotaPerUnit
	}
	costUSD := 0.0
	if cost != nil {
		costUSD = float64(inputTokens)/1_000_000*cost.InputPerMillion +
			float64(outputTokens)/1_000_000*cost.OutputPerMillion
	}
	margin := revenue - costUSD
	pct := 0.0
	if revenue > 0 {
		pct = margin / revenue * 100
	}
	return CostResult{
		RevenueUSD: revenue,
		CostUSD:    costUSD,
		MarginUSD:  margin,
		MarginPct:  pct,
	}
}

func nowUnixCost() int64 { return time.Now().Unix() }

// ListChannelCosts 返回全部渠道单价配置。
func ListChannelCosts() ([]ChannelCost, error) {
	var out []ChannelCost
	err := DB.Model(&ChannelCost{}).Order("channel_id ASC").Find(&out).Error
	return out, err
}

// UpsertChannelCost 按 channel_id 写入或更新单价。
func UpsertChannelCost(c ChannelCost) error {
	c.UpdatedAt = nowUnixCost()
	var existing ChannelCost
	err := DB.Model(&ChannelCost{}).Where("channel_id = ?", c.ChannelId).First(&existing).Error
	if err != nil {
		if createErr := DB.Create(&c).Error; createErr != nil {
			return createErr
		}
		_ = LoadChannelCosts()
		return nil
	}
	err = DB.Model(&ChannelCost{}).Where("channel_id = ?", c.ChannelId).
		Updates(map[string]any{
			"input_per_million":  c.InputPerMillion,
			"output_per_million": c.OutputPerMillion,
			"note":               c.Note,
			"updated_at":         c.UpdatedAt,
		}).Error
	if err == nil {
		_ = LoadChannelCosts()
	}
	return err
}

// DeleteChannelCost 删除某个渠道的单价配置。
func DeleteChannelCost(channelId int) error {
	if err := DB.Where("channel_id = ?", channelId).Delete(&ChannelCost{}).Error; err != nil {
		return err
	}
	_ = LoadChannelCosts()
	return nil
}

// 单价配置在内存里保留一份，读取不再走 MySQL。
//
// 为什么必须这样：margins 是看板接口，每次调用都查一次
// `select * from channel_costs`，等于把「看板的刷新频率」
// 直接变成「MySQL 的连接占用」。这张表只有几十行、每几天才改一次，
// 让它承担这个角色没有道理。
//
// 一致性用「写时刷新」保证：Upsert/Delete 成功后立刻重载，
// 所以运维改完价，下一次读就能看到，不存在 TTL 过期窗口。
var (
	costCacheMu sync.RWMutex
	costCache   []ChannelCost
	costLoaded  bool
)

// LoadChannelCosts 把配置读进内存。启动时调一次，写操作后调一次。
func LoadChannelCosts() error {
	if DB == nil {
		return nil
	}
	var rows []ChannelCost
	if err := DB.Model(&ChannelCost{}).Order("channel_id ASC").Find(&rows).Error; err != nil {
		return err
	}
	costCacheMu.Lock()
	costCache = rows
	costLoaded = true
	costCacheMu.Unlock()
	return nil
}

// CachedChannelCosts 返回内存中的单价配置，按 channel_id 索引。
//
// 未被 Load 过时返回空表而不是报错：成本算成 0 只是「没配价」，
// 让整个看板因为一张配置表报错更糟。
func CachedChannelCosts() map[int]*ChannelCost {
	costCacheMu.RLock()
	defer costCacheMu.RUnlock()
	out := make(map[int]*ChannelCost, len(costCache))
	for i := range costCache {
		out[costCache[i].ChannelId] = &costCache[i]
	}
	return out
}
