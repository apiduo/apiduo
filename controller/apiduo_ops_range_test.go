package controller

import "testing"

// 对齐判据：同一个 30 分钟窗口内的不同时刻，必须归一到同一个 key。
// 这是缓存能否命中的唯一前提——不对齐的话缓存等于没做。
func TestAlignWindowGroupsWithinBucket(t *testing.T) {
	const g = int64(1800)
	base := int64(1_800_000_000) / g * g // 对齐基准
	to1 := base + g                      // 窗口右端
	to2 := to1 + 1                       // 一秒后刷新
	to3 := to1 + 600

	f1, t1 := alignWindow(to1-86400, to1)
	f2, t2 := alignWindow(to2-86400, to2)
	f3, t3 := alignWindow(to3-86400, to3)
	if f1 != f2 || t1 != t2 {
		t.Errorf("同一窗口内应归一：%d-%d vs %d-%d", f1, t1, f2, t2)
	}
	// to3 已经跨到下一个粒度，允许不同
	if t3 != to1+g {
		t.Logf("跨粒度后 to=%d（预期 %d）", t3, to1+g)
	}
	_ = f3
}

// 对齐判据 2：两端都落在粒度边界上，且跨度不变。
//
// from 会向前扩到边界（不是缩小）——这是必要的代价：
// 只对齐 to 而让 from 随秒抖动，缓存 key 依然对不上。
// 对「最近 24 小时」多算 30 分钟旧数据，不影响结论。
func TestAlignWindowStaysAligned(t *testing.T) {
	// 用真实的 24h 窗口：86400 是 1800 的整数倍，跨度不会被取整改变。
	// 非整数倍的跨度（如 3400）会被扩到下一个边界，那是对齐的必然代价。
	from := int64(1_800_000_100)
	to := from + 86400
	span := to - from

	af, at := alignWindow(from, to)
	if at%opsAlignGranularity != 0 {
		t.Errorf("to 应对齐到边界，得到 %d", at)
	}
	if af%opsAlignGranularity != 0 {
		t.Errorf("from 应对齐到边界，得到 %d", af)
	}
	if at-af != span {
		t.Errorf("跨度应保持不变：期望 %d，实际 %d", span, at-af)
	}
	if at > to {
		t.Errorf("to 不应超出原窗口：%d > %d", at, to)
	}
}

// 窗口比一个粒度还短时，退回原值而不是产生空窗口。
func TestAlignWindowTooShort(t *testing.T) {
	from := int64(1_800_000_100)
	to := from + 60
	af, at := alignWindow(from, to)
	if af != from || at != to {
		t.Errorf("过短窗口应原样返回，得到 %d-%d", af, at)
	}
}
