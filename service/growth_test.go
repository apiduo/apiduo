package service

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// 分层切点决定「谁能进 K 层」。切错会让运营把注意力放错人身上，
// 所以边界条件都要固定下来。
func TestPercentileCuts(t *testing.T) {
	cases := []struct {
		total int
		key   int
		act   int
	}{
		{total: 0, key: 0, act: 0},
		{total: 1, key: 1, act: 1},
		{total: 3, key: 1, act: 2},
		{total: 12, key: 1, act: 2},
		{total: 200, key: 6, act: 30},
		{total: 1000, key: 20, act: 150},
	}
	for _, c := range cases {
		key, act := percentileCuts(c.total, DefaultGrowthSettings())
		if key != c.key || act != c.act {
			t.Fatalf("percentileCuts(%d) = (%d,%d), want (%d,%d)", c.total, key, act, c.key, c.act)
		}
	}
}

// K 层人数必须有上限：超过 20 人就失去「能叫出名字」的意义。
func TestPercentileCutsKeyCap(t *testing.T) {
	key, _ := percentileCuts(100000, DefaultGrowthSettings())
	if key > 20 {
		t.Fatalf("key cut %d exceeds cap 20", key)
	}
	if key != 20 {
		t.Fatalf("key cut %d, want 20", key)
	}
}

func TestTrendOf(t *testing.T) {
	cases := []struct {
		last int
		prev int
		want string
	}{
		{last: 0, prev: 0, want: model.GrowthTrendNone},
		{last: 100, prev: 0, want: model.GrowthTrendUp},
		{last: 120, prev: 100, want: model.GrowthTrendUp},
		{last: 100, prev: 100, want: model.GrowthTrendFlat},
		{last: 105, prev: 100, want: model.GrowthTrendFlat},
		{last: 80, prev: 100, want: model.GrowthTrendDown},
		{last: 0, prev: 100, want: model.GrowthTrendDown},
	}
	for _, c := range cases {
		if got := trendOf(c.last, c.prev, 0.1); got != c.want {
			t.Fatalf("trendOf(%d,%d) = %q, want %q", c.last, c.prev, got, c.want)
		}
	}
}

// 沉默账户不参与排名，否则会把 K 层切点挤乱。
func TestAssignTiersSkipsSilent(t *testing.T) {
	rows := []model.GrowthUserBoard{
		{UserId: 1, Quota30d: 9000, LastActiveAt: 2000, Quota7d: 500, QuotaPrev7d: 400},
		{UserId: 2, Quota30d: 5000, LastActiveAt: 2000, Quota7d: 300, QuotaPrev7d: 300},
		{UserId: 3, Quota30d: 0, LastActiveAt: 0},
	}
	assignTiers(rows, 1000, DefaultGrowthSettings())
	if rows[0].Tier != model.GrowthTierKey {
		t.Fatalf("top spender tier = %q, want K", rows[0].Tier)
	}
	if rows[2].Tier != model.GrowthTierSilent {
		t.Fatalf("inactive tier = %q, want S", rows[2].Tier)
	}
	if rows[2].Trend != model.GrowthTrendNone {
		t.Fatalf("inactive trend = %q, want none", rows[2].Trend)
	}
}

// 「有请求但无消耗」必须判成事故型。quota_data 只记成功消费，
// 漏掉这条会把被故障打走的客户静默成沉默账户。
func TestIsIncident(t *testing.T) {
	incident := model.GrowthUserBoard{Requests7d: 120, Quota7d: 0, Failures7d: 120}
	if !isIncident(incident) {
		t.Fatal("requests without consumption must be treated as an incident")
	}
	healthy := model.GrowthUserBoard{Requests7d: 120, Quota7d: 5000}
	if isIncident(healthy) {
		t.Fatal("account with consumption must not be an incident")
	}
	idle := model.GrowthUserBoard{Requests7d: 0, Quota7d: 0}
	if isIncident(idle) {
		t.Fatal("account with no requests must not be an incident")
	}
}

func TestWeekStartAt(t *testing.T) {
	loc := growthLocation()
	// 2026-11-16 是周一。取该周内每一天的随机时刻，都应归到同一个周一 0 点。
	monday := time.Date(2026, 11, 16, 0, 0, 0, 0, loc)
	want := monday.Unix()
	cases := []struct {
		name string
		at   time.Time
	}{
		{"周一凌晨", time.Date(2026, 11, 16, 0, 0, 1, 0, loc)},
		{"周三下午", time.Date(2026, 11, 18, 15, 30, 0, 0, loc)},
		{"周五深夜", time.Date(2026, 11, 20, 23, 59, 59, 0, loc)},
		{"周日晚上", time.Date(2026, 11, 22, 23, 30, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := weekStartAt(c.at.Unix()); got != want {
			t.Errorf("%s: got %d (%s), want %d (%s)", c.name,
				got, time.Unix(got, 0).In(loc).Format("2006-01-02 15:04"),
				want, monday.Format("2006-01-02 15:04"))
		}
	}
	// 下周一必须进入新的一周，不能被归回上一周。
	nextMonday := time.Date(2026, 11, 23, 0, 0, 1, 0, loc)
	if got := weekStartAt(nextMonday.Unix()); got == want {
		t.Errorf("下周一被归到了上一周")
	}
}

// T1 判据 2：换模型的判定必须排除两种情况——
// 首次建立画像（空→有）不是切换；画像丢失（有→空）也不是切换。
func TestModelSwitch(t *testing.T) {
	cases := []struct {
		name string
		prev string
		next string
		want bool
	}{
		{"首次建立画像", "", "gpt-4o", false},
		{"同一模型", "gpt-4o", "gpt-4o", false},
		{"确实换了", "claude-3", "gpt-4o", true},
		{"画像丢失", "gpt-4o", "", false},
		{"大小写不同视为同一模型", "GPT-4o", "gpt-4o", false},
	}
	for _, c := range cases {
		if got := modelSwitch(c.prev, c.next); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// T1 判据 4：换模型预警的文案必须带上前后两个模型，
// 只说「换了模型」运营无法判断这是升级还是准备离开。
func TestModelSwitchAlert(t *testing.T) {
	got := switchDetail("claude-3", "gpt-4o")
	if !strings.Contains(got, "claude-3") || !strings.Contains(got, "gpt-4o") {
		t.Errorf("文案缺少前后模型: %q", got)
	}
	// 未知上一模型时不能出现空箭头
	got2 := switchDetail("", "gpt-4o")
	if strings.Contains(got2, "→  ") || strings.Contains(got2, "从  换") {
		t.Errorf("上一模型为空时文案有空洞: %q", got2)
	}
}

// 第一批判据 2：失败率要相对自己的基线看，不能只看绝对值。
// 绝对阈值的两个失效方向：常态就高的客户天天告警；常态极低的客户
// 从 0.01% 涨到 0.8%（80 倍）反而不告警。
func TestFailureSpike(t *testing.T) {
	s := DefaultGrowthSettings()
	cases := []struct {
		name  string
		r7    float64
		r30   float64
		want  bool
	}{
		{"常态 5%，升到 5.2% — 正常波动，不报", 0.052, 0.05, false},
		{"常态 5%，升到 22% — 绝对超标，报", 0.22, 0.05, true},
		{"常态 0.01%，升到 0.8% — 相对恶化 80 倍，报", 0.008, 0.0001, true},
		{"常态 0.01%，升到 0.05% — 倍数够但绝对值太小，是小样本噪声，不报", 0.0005, 0.0001, false},
		{"常态 1%，升到 25% — 绝对超标，报", 0.25, 0.01, true},
		{"基线为零、近7天 3% — 无基线可比，按绝对下限报", 0.03, 0, true},
		{"全零 — 不报", 0, 0, false},
	}
	for _, c := range cases {
		if got := isFailureSpike(c.r7, c.r30, s); got != c.want {
			t.Errorf("%s: got %v, want %v (r7=%.4f r30=%.4f)", c.name, got, c.want, c.r7, c.r30)
		}
	}
}
