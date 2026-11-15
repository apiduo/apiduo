package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

// 签名密钥必须在库里：重启或换副本后，旧 task_id 仍要能解开。
// 这里清空进程内缓存来模拟「另一个副本」，验的是持久化而不是内存。
func TestTaskIDSignedKeySurvivesRestart(t *testing.T) {
	setupDB(t)

	signed, err := signTaskID(taskPayload{
		Model: "vid", Upstream: "up-1", Kind: "video", UserId: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	var opt model.Option
	if err := model.DB.Where("key = ?", taskSigningKeyOption).First(&opt).Error; err != nil {
		t.Fatalf("签名密钥没有入库: %v", err)
	}
	if len(opt.Value) != 64 {
		t.Fatalf("密钥应是 32 字节的 hex（64 字符），实际 %d", len(opt.Value))
	}

	// 模拟换进程 / 换副本
	resetTaskSigningKey()

	p, err := parseTaskID(signed, 7)
	if err != nil {
		t.Fatalf("重读密钥后应能解开: %v", err)
	}
	if p.Upstream != "up-1" || p.Model != "vid" {
		t.Fatalf("payload 不对: %+v", p)
	}
}

// 归属校验：别人拿到 task_id 也读不到。
func TestTaskIDRejectsForeignUser(t *testing.T) {
	setupDB(t)

	signed, err := signTaskID(taskPayload{
		Model: "vid", Upstream: "up-1", Kind: "video", UserId: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseTaskID(signed, 8); err != errTaskIDForeign {
		t.Fatalf("换个用户应被拒，实际 %v", err)
	}
}

// 句柄有寿命：过期后给出专门的码，而不是笼统的「无效」。
func TestTaskIDRejectsExpired(t *testing.T) {
	setupDB(t)

	signed, err := signTaskID(taskPayload{
		Model: "vid", Upstream: "up-1", Kind: "video",
		ExpireAt: time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseTaskID(signed, 0); err != errTaskIDExpired {
		t.Fatalf("过期应被拒，实际 %v", err)
	}
}

// 端到端：过期句柄走工具时回 TASK_ID_EXPIRED，且带结构化错误体。
func TestTaskGetExpiredIsStructured(t *testing.T) {
	setupDB(t)
	srv := taskGetServer(t, "unused", `{}`)

	signed, err := signTaskID(taskPayload{
		Model: "text-video", Upstream: "task-x", Kind: "video",
		ExpireAt: time.Now().Add(-time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": signed},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("过期句柄应回 isError: %v", res)
	}
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeTaskIDExpired {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeTaskIDExpired)
	}
	if txt := toolText(t, res); !strings.Contains(txt, "已过期") {
		t.Fatalf("text 应说明过期，实际 %q", txt)
	}
}

// 端到端：别人的句柄回 TASK_ID_INVALID，文案点明归属问题。
func TestTaskGetForeignUserIsStructured(t *testing.T) {
	setupDB(t)
	srv := taskGetServer(t, "unused", `{}`)

	signed, err := signTaskID(taskPayload{
		Model: "text-video", Upstream: "task-x", Kind: "video", UserId: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "task.get", "arguments": map[string]any{"task_id": signed},
	})
	sc, _ := res["structuredContent"].(map[string]any)
	terr, _ := sc["error"].(map[string]any)
	if code, _ := terr["code"].(string); code != CodeTaskIDInvalid {
		t.Fatalf("code = %v，期望 %s", terr["code"], CodeTaskIDInvalid)
	}
	if txt := toolText(t, res); !strings.Contains(txt, "不属于当前用户") {
		t.Fatalf("text 应点明归属，实际 %q", txt)
	}
}

// key 是 MySQL 保留字：这条守的是查询必须带标识符引号。
// SQLite 不引号也能跑，所以只能查生成的 SQL。
func TestOptionQueryQuotesReservedWord(t *testing.T) {
	setupDB(t)
	stmt := optionQuery(taskSigningKeyOption).
		Session(&gorm.Session{DryRun: true}).
		First(&model.Option{}).Statement
	sql := stmt.SQL.String()
	if !strings.Contains(sql, "`key`") && !strings.Contains(sql, `"key"`) {
		t.Fatalf("options.key 查询没有加引号，MySQL 会报 1064: %s", sql)
	}
}
