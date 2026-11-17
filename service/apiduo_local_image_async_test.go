package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Worker 里的一次 panic 不能带走进程：这条 goroutine 处理的是上游报文，
// 一个 nil 解引用会连带打死网关上所有无关请求。
func TestApiduoRunLocalTaskGuardedContainsPanic(t *testing.T) {
	task := &model.Task{TaskID: "task_guarded"}

	recovered := apiduoRunLocalTaskGuarded(context.Background(), task, func() {
		panic("boom")
	})
	if recovered == nil {
		t.Fatal("panic 必须被捕获并返回")
	}

	// 正常执行不应报告 panic。
	if recovered := apiduoRunLocalTaskGuarded(context.Background(), task, func() {}); recovered != nil {
		t.Fatalf("正常执行被误判成 panic: %v", recovered)
	}

	// nil 任务也不能让日志那一行炸掉。
	if recovered := apiduoRunLocalTaskGuarded(context.Background(), nil, func() { panic("nil task") }); recovered == nil {
		t.Fatal("panic 必须被捕获")
	}
}

// 并发度是配置项：默认 8，非法值回落默认，超大值被上限截断。
func TestApiduoLocalImageAsyncConcurrency(t *testing.T) {
	cases := map[string]int{
		"":    apiduoLocalImageAsyncDefaultConcurrency,
		"16":  16,
		"0":   apiduoLocalImageAsyncDefaultConcurrency,
		"-3":  apiduoLocalImageAsyncDefaultConcurrency,
		"abc": apiduoLocalImageAsyncDefaultConcurrency,
		"999": apiduoLocalImageAsyncMaxConcurrency,
	}
	for raw, want := range cases {
		t.Setenv("LOCAL_IMAGE_ASYNC_CONCURRENCY", raw)
		if got := apiduoLocalImageAsyncConcurrency(); got != want {
			t.Errorf("LOCAL_IMAGE_ASYNC_CONCURRENCY=%q => %d, want %d", raw, got, want)
		}
	}
}

// 认领与回收必须共用一个时钟：一边写「我开始于」，另一边问「这是不是太旧了」。
// 两边读不同的钟，几分钟偏移就能把活任务判死。
func TestApiduoClaimLocalTaskUsesTheSharedClock(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tasks.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	task := &model.Task{TaskID: "task_clock", Status: model.TaskStatusNotStart, ExecutionMode: model.TaskExecutionModeLocal}
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	previousClock := apiduoLocalImageAsyncClock
	apiduoLocalImageAsyncClock = func() int64 { return 1000 }
	t.Cleanup(func() { apiduoLocalImageAsyncClock = previousClock })

	if !apiduoClaimLocalTask(task) {
		t.Fatal("claim should win on a fresh row")
	}
	var stored model.Task
	if err := db.Where("task_id = ?", "task_clock").First(&stored).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.StartTime != 1000 {
		t.Fatalf("start_time = %d, want the injected clock value 1000", stored.StartTime)
	}
	if stored.Status != model.TaskStatusInProgress {
		t.Fatalf("status = %s", stored.Status)
	}
}

// 回收截止点由同一个时钟推出：超时 5 分钟 + 1 分钟余量。
func TestApiduoLocalImageAsyncReapCutoff(t *testing.T) {
	const now = int64(1_000_000)
	if got := apiduoLocalImageAsyncReapCutoff(now); got != now-360 {
		t.Fatalf("cutoff = %d, want %d", got, now-360)
	}
}

// 轮转只改顺序，不改每个渠道内部的先后，也不丢任务。
func TestApiduoLocalImageAsyncInterleaved(t *testing.T) {
	build := func(channelID int, ids ...string) []*model.Task {
		out := make([]*model.Task, 0, len(ids))
		for _, id := range ids {
			out = append(out, &model.Task{TaskID: id, ChannelId: channelID})
		}
		return out
	}

	// 单渠道：顺序原样（单供应商不该被降速）。
	single := build(1, "a1", "a2", "a3")
	got := apiduoLocalImageAsyncInterleaved(single)
	for i := range single {
		if got[i].TaskID != single[i].TaskID {
			t.Fatalf("single channel order changed: %v", got)
		}
	}

	// 双渠道：交替，且各自保序。
	mixed := append(build(1, "a1", "a2", "a3"), build(2, "b1", "b2")...)
	got = apiduoLocalImageAsyncInterleaved(mixed)
	want := []string{"a1", "b1", "a2", "b2", "a3"}
	if len(got) != len(want) {
		t.Fatalf("length %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].TaskID != id {
			t.Fatalf("position %d = %s, want %s (%v)", i, got[i].TaskID, id, got)
		}
	}
}

// 请求体按「执行时那条渠道」重新映射：运营配映射就是为了让这条渠道能参与，
// 把它排除在轮换之外正好相反。
func TestApiduoLocalImageBodyPreparesPerChannel(t *testing.T) {
	mapping := `{"client-model":"vendor-model"}`
	channel := &model.Channel{Id: 7}
	channel.ModelMapping = &mapping

	task := &model.Task{TaskID: "task_body"}
	task.Properties.OriginModelName = "client-model"
	task.PrivateData.Execution = &model.TaskExecutionSnapshot{
		OriginRequest: []byte(`{"model":"client-model","seed":9007199254740993,"prompt":"x"}`),
		Request:       []byte(`{"model":"submitting-channel-name"}`),
	}

	body, upstream, err := apiduoLocalImageBody(channel, task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if upstream != "vendor-model" {
		t.Fatalf("upstream = %q, want vendor-model", upstream)
	}
	if !strings.Contains(string(body), `"model":"vendor-model"`) {
		t.Fatalf("body was not re-mapped: %s", body)
	}
	// 大整数不能被 float64 舍入：换种子就是换图。
	if !strings.Contains(string(body), "9007199254740993") {
		t.Fatalf("seed lost precision: %s", body)
	}
}

// 老行没有 origin：按原样发送，不去猜。
func TestApiduoLocalImageBodyFallsBackToStoredRequest(t *testing.T) {
	channel := &model.Channel{Id: 8}
	task := &model.Task{TaskID: "task_legacy"}
	task.PrivateData.Execution = &model.TaskExecutionSnapshot{Request: []byte(`{"model":"already-mapped"}`)}

	body, _, err := apiduoLocalImageBody(channel, task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != `{"model":"already-mapped"}` {
		t.Fatalf("legacy body changed: %s", body)
	}
}
