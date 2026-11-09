package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// ShouldEnableChannel 是「自动启用」的唯一闸门：批量测试 / 定时测试跑完一个渠道后，
// 只有它返回 true 才会把 status 从 3 翻回 1。
//
// 所以每条不满足的分支都必须明确返回 false —— 把一个**人工禁用**的渠道悄悄翻回来，
// 比不启用更糟：管理员看到的是「我明明关了，它自己又开了」。e2e 只能验到
// 「批量测试成功 → 从 3 变 1」和「人工禁用不被改写」两条，剩下的纯函数分支在这里钉。
func TestShouldEnableChannel(t *testing.T) {
	upstreamError := &types.NewAPIError{}

	tests := []struct {
		name     string
		switchOn bool
		err      *types.NewAPIError
		status   int
		want     bool
	}{
		{"开关打开、无错、自动禁用 → 启用", true, nil, common.ChannelStatusAutoDisabled, true},
		{"开关关闭 → 不启用", false, nil, common.ChannelStatusAutoDisabled, false},
		{"测试失败 → 不启用", true, upstreamError, common.ChannelStatusAutoDisabled, false},
		{"人工禁用 → 不碰", true, nil, common.ChannelStatusManuallyDisabled, false},
		{"本来就是启用 → 不需要动作", true, nil, common.ChannelStatusEnabled, false},
		{"未知状态 → 不碰", true, nil, 0, false},
	}

	original := common.AutomaticEnableChannelEnabled
	t.Cleanup(func() { common.AutomaticEnableChannelEnabled = original })

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			common.AutomaticEnableChannelEnabled = tt.switchOn
			if got := ShouldEnableChannel(tt.err, tt.status); got != tt.want {
				t.Fatalf("ShouldEnableChannel(err=%v, status=%d) = %v, want %v",
					tt.err, tt.status, got, tt.want)
			}
		})
	}
}

// 自动禁用和自动启用是一对，共享同一个「渠道不可用」判定。这里只钉住
// 开关关闭时**绝不**禁用，避免把配置错误当成渠道故障。
func TestShouldDisableChannelRespectsTheSwitch(t *testing.T) {
	original := common.AutomaticDisableChannelEnabled
	t.Cleanup(func() { common.AutomaticDisableChannelEnabled = original })

	upstreamError := &types.NewAPIError{}
	common.AutomaticDisableChannelEnabled = false
	if ShouldDisableChannel(upstreamError) {
		t.Fatal("开关关闭时仍然判定要禁用渠道")
	}
}
