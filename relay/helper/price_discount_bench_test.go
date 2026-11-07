package helper

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

func benchRules(n int) json.RawMessage {
	if n == 0 {
		return nil
	}
	rules := make([]map[string]any, 0, n)
	for i := range n {
		rules = append(rules, map[string]any{
			"groups": "vip",
			"models": fmt.Sprintf("bench-model-%d", i),
			"rate":   0.8,
			"until":  0,
		})
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		panic(err)
	}
	return raw
}

// BenchmarkModelPriceHelper 量化折扣对热路径的影响：
// 无折扣走 nil 短路，有折扣才是每请求一次 JSON 解析 + 线性匹配。
func BenchmarkModelPriceHelper(b *testing.B) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	if err := config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	savedModelRatio := ratio_setting.ModelRatio2JSONString()
	b.Cleanup(func() {
		_ = config.GlobalConfig.LoadFromDB(saved)
		_ = ratio_setting.UpdateModelRatioByJSONString(savedModelRatio)
	})
	if err := config.GlobalConfig.LoadFromDB(map[string]string{
		"group_ratio_setting.group_ratio": `{"default":1,"vip":0.9}`,
	}); err != nil {
		b.Fatal(err)
	}
	if err := ratio_setting.UpdateModelRatioByJSONString(`{"bench-model-0":1,"bench-model-49":1}`); err != nil {
		b.Fatal(err)
	}

	c, _ := gin.CreateTestContext(nil)

	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"no-discount", nil},
		{"1-rule", benchRules(1)},
		{"5-rules", benchRules(5)},
		{"20-rules-max", benchRules(20)},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				info := &relaycommon.RelayInfo{
					OriginModelName:     "bench-model-0",
					UserGroup:           "vip",
					UsingGroup:          "vip",
					RelayFormat:         types.RelayFormatOpenAI,
					BillingRequestInput: &billingexpr.RequestInput{},
					UserSetting:         dto.UserSetting{ApiduoDiscounts: tc.raw},
				}
				if _, err := ModelPriceHelper(c, info, 1000, &types.TokenCountMeta{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkHandleGroupRatio 只看组倍率这一步，隔离出折扣本身的成本。
func BenchmarkHandleGroupRatio(b *testing.B) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"no-discount", nil},
		{"5-rules-hit", benchRules(5)},
		{"20-rules-miss", benchRules(20)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			info := &relaycommon.RelayInfo{
				OriginModelName: "bench-model-0",
				UserGroup:       "vip",
				UsingGroup:      "vip",
				UserSetting:     dto.UserSetting{ApiduoDiscounts: tc.raw},
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = HandleGroupRatio(c, info)
			}
		})
	}
}
