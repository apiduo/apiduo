package controller

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// 定时「测试所有渠道」的开关与节奏都来自 monitor 设置。minutes 配成 0 或负数时
// 必须回落到 10 分钟，否则 system task 的 ticker 会拿到 0 间隔自旋。
// 这条边界只有这里能钉：e2e 跑不到分钟级 tick。
func TestChannelTestHandlerScheduling(t *testing.T) {
	setting := operation_setting.GetMonitorSetting()
	original := *setting
	t.Cleanup(func() { *setting = original })

	handler := channelTestHandler{}

	setting.AutoTestChannelEnabled = true
	if !handler.Enabled() {
		t.Fatal("开关打开时定时任务却判定为关闭")
	}
	setting.AutoTestChannelEnabled = false
	if handler.Enabled() {
		t.Fatal("开关关闭时定时任务仍在跑")
	}

	setting.AutoTestChannelMinutes = 7
	if got := handler.Interval(); got != 7*time.Minute {
		t.Fatalf("Interval() = %v, 期望 7m", got)
	}
	for _, minutes := range []float64{0, -3} {
		setting.AutoTestChannelMinutes = minutes
		if got := handler.Interval(); got != 10*time.Minute {
			t.Fatalf("minutes=%v 时应回落 10m，got %v", minutes, got)
		}
	}
}
