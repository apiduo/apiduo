package controller

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// BenchmarkPricingResponseMarshal 量出 /api/pricing 响应的序列化成本，
// 用来判断折扣展开（约 1.85µs）在整条响应里占多大比例。
func BenchmarkPricingResponseMarshal(b *testing.B) {
	items := make([]model.Pricing, 300)
	for i := range items {
		items[i] = model.Pricing{
			ModelName:       fmt.Sprintf("bench-model-%d", i),
			ModelRatio:      1.5,
			CompletionRatio: 2,
			EnableGroup:     []string{"default", "vip"},
			OwnerBy:         "system",
			Tags:            "chat",
			Description:     "benchmark fixture",
		}
	}
	payload := gin.H{
		"success":         true,
		"data":            items,
		"vendors":         []any{},
		"group_ratio":     map[string]float64{"default": 1, "vip": 0.9},
		"usable_group":    map[string]any{},
		"auto_groups":     []string{},
		"pricing_version": "a42d372ccf0b5dd13ecf71203521f9d2",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := json.Marshal(payload); err != nil {
			b.Fatal(err)
		}
	}
}
