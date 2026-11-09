package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// 别名路由靠这个判定决定「这条方舟形状的提交归渠道还是归插件」：
// 判错就会把请求送到插件（客户端收到不相干的 404），或者把插件该管的模型抢过来。
func TestApiduoAdvancedAsyncServes(t *testing.T) {
	previous := channelsIDM
	t.Cleanup(func() { channelsIDM = previous })

	asyncSettings := `{"advanced_async":{"submit":{"upstream_path":"/v1/tasks"},"query":{"upstream_path":"/v1/tasks/{task_id}"}}}`
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "seedance-x, Seedream-Y", OtherSettings: asyncSettings},
		2: {Id: 2, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusManuallyDisabled,
			Models: "disabled-model", OtherSettings: asyncSettings},
		3: {Id: 3, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "unconfigured-model"}, // 没有 advanced_async：这条渠道不该被当成可用
		4: {Id: 4, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled,
			Models: "custom-model", OtherSettings: asyncSettings},
	}

	cases := []struct {
		name  string
		model string
		want  bool
	}{
		{"声明过的模型", "seedance-x", true},
		{"模型名大小写不敏感", "seedream-y", true},
		{"前后空白不影响", "  seedance-x  ", true},
		{"未声明的模型", "not-listed", false},
		{"禁用渠道不算", "disabled-model", false},
		{"没配 advanced_async 的渠道不算", "unconfigured-model", false},
		{"别的渠道类型不算", "custom-model", false},
		{"空模型名", "", false},
	}

	for _, tc := range cases {
		if got := ApiduoAdvancedAsyncServes(tc.model); got != tc.want {
			t.Fatalf("%s: ApiduoAdvancedAsyncServes(%q) = %v, want %v", tc.name, tc.model, got, tc.want)
		}
	}

	// "*" 是这类渠道的「任意模型」写法：它必须接住任何名字，否则方舟形状的
	// 提交会在没有任何通配渠道时落到插件上。
	channelsIDM = map[int]*Channel{
		5: {Id: 5, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "*", OtherSettings: asyncSettings},
	}
	if !ApiduoAdvancedAsyncServes("whatever-1") {
		t.Fatal("wildcard channel must claim any model")
	}
}
