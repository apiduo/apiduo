package helper

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestModelPriceHelperDiscountAffectsPreConsume 验证折扣不只改了 GroupRatio 字段，
// 而是真的进入了预扣额度（ratio := modelRatio * GroupRatio）。
func TestModelPriceHelperDiscountAffectsPreConsume(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedConfig := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		savedConfig[key] = value
		return nil
	}))
	savedModelRatio := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(savedConfig))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatio))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"discount-flow-model":1}`))

	run := func(raw string) int {
		c, _ := gin.CreateTestContext(nil)
		info := &relaycommon.RelayInfo{
			OriginModelName:     "discount-flow-model",
			UserGroup:           "default",
			UsingGroup:          "default",
			RelayFormat:         types.RelayFormatOpenAI,
			UserSetting:         dto.UserSetting{ApiduoDiscounts: json.RawMessage(raw)},
			BillingRequestInput: &billingexpr.RequestInput{},
		}
		priceData, err := ModelPriceHelper(c, info, 1000, &types.TokenCountMeta{})
		require.NoError(t, err)
		return priceData.QuotaToPreConsume
	}

	base := run("")
	half := run(`[{"groups":"default","models":"discount-flow-model","rate":0.5}]`)

	require.NotZero(t, base, "基准预扣不应为 0")
	assert.InDelta(t, float64(base)/2, float64(half), 1, "五折预扣应约为原价一半")
}
