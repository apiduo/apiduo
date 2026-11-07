package helper

import (
	"encoding/json"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleGroupRatioAppliesUserDiscount 固定三个语义：
//  1. 折扣覆盖最终组倍率，而不是在组倍率上相乘；
//  2. 折扣比 group_group_ratio 更具体，命中时覆盖它；
//  3. 规则非法 / 过期 / 未命中时，完整回落到原有链路，不影响其它设置。
func TestHandleGroupRatioAppliesUserDiscount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"group_ratio_setting.group_ratio":       `{"default":1,"vip":0.9,"svip":1}`,
		"group_ratio_setting.group_group_ratio": `{"vip":{"svip":0.9}}`,
	}))

	rules := `[
		{"groups":"vip","models":"gpt-4o","rate":0.70},
		{"groups":"vip,svip","models":"claude-3.7,gemini-2.5","rate":0.85},
		{"groups":"vip","models":"*","rate":0.80},
		{"groups":"svip","models":"*","rate":0.90},
		{"groups":"*","models":"*","rate":0.95}
	]`

	cases := []struct {
		name        string
		userGroup   string
		usingGroup  string
		model       string
		raw         string
		want        float64
		wantSpecial bool
	}{
		{"精确命中且覆盖而非相乘", "vip", "vip", "gpt-4o", rules, 0.70, true},
		{"组保底", "vip", "vip", "deepseek-r1", rules, 0.80, true},
		{"跨组模型例外", "vip", "vip", "claude-3.7", rules, 0.85, true},
		{"svip 保底", "vip", "svip", "gpt-4o", rules, 0.90, true},
		{"未命中回落组价", "vip", "default", "gpt-4o", `[{"groups":"vip","models":"gpt-4o","rate":0.70}]`, 1.0, false},
		{"兜底规则覆盖其他组", "vip", "default", "gpt-4o", rules, 0.95, true},
		{"无规则回落组价", "vip", "vip", "gpt-4o", ``, 0.9, false},
		{"坏 JSON 只丢折扣", "vip", "vip", "gpt-4o", `{bad`, 0.9, false},
		{"过期回落组价", "vip", "vip", "gpt-4o",
			`[{"groups":"vip","models":"gpt-4o","rate":0.70,"until":1}]`, 0.9, false},
		{"折扣覆盖 group_group_ratio", "vip", "svip", "gpt-4o",
			`[{"groups":"svip","models":"*","rate":0.60}]`, 0.60, true},
		{"无折扣时 group_group_ratio 生效", "vip", "svip", "gpt-4o", ``, 0.9, true},
		{"空模型名不命中", "vip", "vip", "", rules, 0.9, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(nil)
			info := &relaycommon.RelayInfo{
				UserGroup:        tc.userGroup,
				UsingGroup:       tc.usingGroup,
				BillingModelName: tc.model,
				UserSetting:      dto.UserSetting{ApiduoDiscounts: json.RawMessage(tc.raw)},
			}

			got := HandleGroupRatio(c, info)

			assert.InDelta(t, tc.want, got.GroupRatio, 1e-9)
			assert.Equal(t, tc.wantSpecial, got.HasSpecialRatio)
			if tc.wantSpecial {
				assert.InDelta(t, tc.want, got.GroupSpecialRatio, 1e-9)
			} else {
				assert.InDelta(t, -1.0, got.GroupSpecialRatio, 1e-9)
			}
		})
	}
}
