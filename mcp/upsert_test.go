package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

func patchOf(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &patch); err != nil {
		t.Fatalf("测试用例的 JSON 不合法: %v", err)
	}
	if len(patch) == 0 {
		t.Fatal("测试用例不能是空 patch")
	}
	return patch
}

func versionOf(v int64) *int64 { return &v }

// 一次调用完成「没有就建、有就改」，且业务键带斜杠也能用。
//
// 斜杠不是凑数的用例：openai/gpt-image-1 这类 ID 在真实渠道里很常见，
// 而它正是「业务键不能放进路径段」的原因。
func TestUpsertCreatesByModelId(t *testing.T) {
	setupDB(t)

	p, created, err := UpsertProfileByModelId(
		"openai/gpt-image-1",
		patchOf(t, `{"kind":"image","display_name":"GPT Image"}`),
		nil,
	)
	if err != nil {
		t.Fatalf("upsert 失败: %v", err)
	}
	if !created {
		t.Fatal("首次写入应当回报 created=true")
	}
	if p.ModelId != "openai/gpt-image-1" {
		t.Errorf("业务键被改写: %q", p.ModelId)
	}
	if !p.Enabled {
		t.Error("纳管通道新建的声明应当默认启用，否则 agent 看不到它")
	}
	if p.Version != 1 {
		t.Errorf("新建的 version 应当是 1，实际 %d", p.Version)
	}
	if p.Protocol != "ark" {
		t.Errorf("protocol 应当按 kind 兜底成 ark，实际 %q", p.Protocol)
	}
}

// 第二次调用同一份 patch：不新建、不报错、版本前进一格。
func TestUpsertIsIdempotent(t *testing.T) {
	setupDB(t)

	first, _, err := UpsertProfileByModelId("m-idem", patchOf(t, `{"kind":"video","display_name":"V1"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, created, err := UpsertProfileByModelId("m-idem", patchOf(t, `{"display_name":"V1"}`), nil)
	if err != nil {
		t.Fatalf("重复 upsert 不该失败: %v", err)
	}
	if created {
		t.Error("已存在的声明不该回报 created=true")
	}
	if second.Id != first.Id {
		t.Errorf("同一业务键落到了两行: %d vs %d", first.Id, second.Id)
	}
	if second.Version != first.Version+1 {
		t.Errorf("version 应当 +1：%d → %d", first.Version, second.Version)
	}
}

// patch 语义：没出现的键一个都不能动，尤其是 enabled。
//
// 这条是防回归的重点 —— 整份覆盖会让「agent 只想改简介」顺手把模型停用，
// 而回包还是 200。
func TestUpsertPatchesOnlyGivenKeys(t *testing.T) {
	setupDB(t)

	limit := 3
	created, _, err := UpsertProfileByModelId("m-patch", patchOf(t,
		`{"kind":"image","display_name":"原名","summary":"原简介","max_ref_images":3}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.MaxRefImages == nil || *created.MaxRefImages != limit {
		t.Fatalf("前置条件没建立：max_ref_images=%v", created.MaxRefImages)
	}
	if err := SetProfileEnabled(created.Id, false); err != nil {
		t.Fatal(err)
	}

	got, _, err := UpsertProfileByModelId("m-patch", patchOf(t, `{"summary":"新简介"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "新简介" {
		t.Errorf("给定的键没写进去: %q", got.Summary)
	}
	if got.DisplayName != "原名" {
		t.Errorf("没给的键被覆盖了: %q", got.DisplayName)
	}
	if got.MaxRefImages == nil || *got.MaxRefImages != limit {
		t.Errorf("三态字段被改成了 %v", got.MaxRefImages)
	}
	if got.Enabled {
		t.Error("upsert 不该动 enabled —— 启停是独立动作")
	}
}

// 版本对不上就是冲突，而且一行都不能改。
func TestUpsertRejectsStaleVersion(t *testing.T) {
	setupDB(t)

	p, _, err := UpsertProfileByModelId("m-lock", patchOf(t, `{"kind":"image","display_name":"old"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟另一个写入者先改了一版。
	if _, _, err := UpsertProfileByModelId("m-lock", patchOf(t, `{"summary":"别人的写入"}`), nil); err != nil {
		t.Fatal(err)
	}

	_, _, err = UpsertProfileByModelId("m-lock", patchOf(t, `{"display_name":"我的写入"}`), versionOf(p.Version))
	if !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("应当返回 ErrProfileConflict，实际 %v", err)
	}

	after, ok := GetProfileByModelId("m-lock")
	if !ok {
		t.Fatal("冲突后记录不该消失")
	}
	if after.DisplayName != "old" {
		t.Errorf("冲突的写入落库了: %q", after.DisplayName)
	}
	if after.Summary != "别人的写入" {
		t.Errorf("别人的写入被覆盖: %q", after.Summary)
	}
}

// expected_version=0 是「我要求它不存在」。已经存在就必须冲突，而不是悄悄改成更新。
func TestUpsertExpectedZeroConflictsOnExisting(t *testing.T) {
	setupDB(t)

	if _, _, err := UpsertProfileByModelId("m-expect0", patchOf(t, `{"kind":"image"}`), versionOf(0)); err != nil {
		t.Fatalf("不存在时的 expected_version=0 应当成功: %v", err)
	}
	_, created, err := UpsertProfileByModelId("m-expect0", patchOf(t, `{"display_name":"x"}`), versionOf(0))
	if !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("应当返回 ErrProfileConflict，实际 %v", err)
	}
	if created {
		t.Error("冲突不该被当成新建")
	}
}

// 调用方声称这条存在（expected_version>0），库里却没有：说明刚被删，只能是冲突。
func TestUpsertExpectedVersionOnMissingConflicts(t *testing.T) {
	setupDB(t)

	_, _, err := UpsertProfileByModelId("m-gone", patchOf(t, `{"kind":"image"}`), versionOf(7))
	if !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("应当返回 ErrProfileConflict，实际 %v", err)
	}
	if _, ok := GetProfileByModelId("m-gone"); ok {
		t.Fatal("冲突路径不该顺手建出一条")
	}
}

// 新建也要过形状校验 —— 否则「直连 API 能写出前端造不出来的声明」会在新入口重演。
func TestUpsertValidatesShape(t *testing.T) {
	setupDB(t)

	cases := map[string]string{
		"产物类型":    `{"kind":"audio"}`,
		"负数上限":    `{"kind":"image","max_ref_images":-1}`,
		"参数 JSON": `{"kind":"image","parameters":"{not json}"}`,
	}
	for name, body := range cases {
		_, _, err := UpsertProfileByModelId("m-bad", patchOf(t, body), nil)
		if err == nil {
			t.Errorf("%s：应当被拒", name)
			continue
		}
		var ue userErr
		if !errors.As(err, &ue) {
			t.Errorf("%s：应当是可直接展示的 userErr，实际 %T", name, err)
		}
		if _, ok := GetProfileByModelId("m-bad"); ok {
			t.Fatalf("%s：校验失败却落了库", name)
		}
	}
}

// 更新路径的栅栏也能直接验证：拿一个过期的版本写，必须一行不改。
func TestWriteProfileColumnsHonoursVersion(t *testing.T) {
	setupDB(t)

	p, _, err := UpsertProfileByModelId("m-fence", patchOf(t, `{"kind":"image","display_name":"v1"}`), nil)
	if err != nil {
		t.Fatal(err)
	}

	stale := p
	stale.DisplayName = "v2"
	if err := writeProfileColumns(&stale, versionOf(p.Version-1)); !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("过期版本应当被拒，实际 %v", err)
	}
	if got, _ := GetProfile(p.Id); got.DisplayName != "v1" {
		t.Errorf("被拒的写入落库了: %q", got.DisplayName)
	}
}

// upsert 是控制面写入口，审计必须能回答「谁把哪个模型改成了什么」。
// 这里只锁「操作人能落到字段上」这一层，审计表本身由 model 侧保证。
func TestUpsertReportsExistingProfile(t *testing.T) {
	setupDB(t)

	if _, _, err := UpsertProfileByModelId("m-view", patchOf(t, `{"kind":"search"}`), nil); err != nil {
		t.Fatal(err)
	}
	before, existed := GetProfileByModelId("m-view")
	if !existed {
		t.Fatal("写入后应当能读到")
	}
	if before.Kind != "search" {
		t.Errorf("kind 落库错误: %q", before.Kind)
	}
}

// ── HTTP 层 ────────────────────────────────────────────────────────────

// 409 必须同时给出可判别的 code 和当前值：agent 拿到就能在 current 上重放
// patch，而不是再发一次 GET 去猜「到底谁改了什么」。
func TestUpsertHandlerConflictResponse(t *testing.T) {
	setupDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/api/mcp/profiles/by-model", apiUpsertProfile)

	call := func(query, body string) (int, map[string]any) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/mcp/profiles/by-model?"+query, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("回包不是 JSON: %v / %s", err, w.Body.String())
		}
		return w.Code, out
	}

	code, out := call("model_id=openai/gpt-image-1", `{"kind":"image","display_name":"a"}`)
	if code != http.StatusOK || out["success"] != true {
		t.Fatalf("首次写入应当 200+success，实际 %d %v", code, out)
	}
	data, _ := out["data"].(map[string]any)
	if data["created"] != true {
		t.Errorf("首次写入应当标 created=true，实际 %v", data["created"])
	}
	profile, _ := data["profile"].(map[string]any)
	if profile["model_id"] != "openai/gpt-image-1" {
		t.Errorf("业务键丢了: %v", profile["model_id"])
	}
	if profile["version"] != float64(1) {
		t.Errorf("回包应当带 version=1，实际 %v", profile["version"])
	}

	code, out = call("model_id=openai/gpt-image-1&expected_version=0", `{"display_name":"b"}`)
	if code != http.StatusConflict {
		t.Fatalf("过期版本应当 409，实际 %d %v", code, out)
	}
	if out["code"] != "MCP_PROFILE_VERSION_CONFLICT" {
		t.Errorf("缺少机器可判别的 code，实际 %v", out["code"])
	}
	conflict, _ := out["data"].(map[string]any)
	current, _ := conflict["current"].(map[string]any)
	if current["display_name"] != "a" {
		t.Errorf("409 应当带回当前值，实际 %v", current)
	}
}

// 升级前的行 version 是 0，而 0 在 upsert 里表示「要求不存在」——
// 不把它抬到 1，agent 读到 0 再带 0 写就会被误判成冲突。
func TestMigrateLiftsLegacyVersionToZeroFree(t *testing.T) {
	db := setupDB(t)

	legacy := ModelProfile{ModelId: "m-legacy", Kind: "image", Protocol: "ark", Enabled: true}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	// Create 会写 Version=0，正是升级前那条老数据的形状。
	if err := db.Model(&ModelProfile{}).Where("id = ?", legacy.Id).Update("version", 0).Error; err != nil {
		t.Fatal(err)
	}

	if err := Migrate(); err != nil {
		t.Fatalf("Migrate 失败: %v", err)
	}

	got, ok := GetProfileByModelId("m-legacy")
	if !ok {
		t.Fatal("老数据丢了")
	}
	if got.Version == 0 {
		t.Fatal("老数据的 version 仍是 0 —— expected_version=0 会把它当成「不存在」")
	}
	// 抬到 1 之后，带 0 写必须冲突（语义唯一），带 1 写必须成功。
	if _, _, err := UpsertProfileByModelId("m-legacy", patchOf(t, `{"summary":"x"}`), versionOf(0)); !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("expected_version=0 应当冲突，实际 %v", err)
	}
	if _, _, err := UpsertProfileByModelId("m-legacy", patchOf(t, `{"summary":"x"}`), versionOf(got.Version)); err != nil {
		t.Fatalf("带正确版本应当成功: %v", err)
	}
}

// 冲突必须留痕，而且不能是一条「成功」的审计。
//
// 鉴权链路的兜底审计只记 method+route 的 generic，回答不了「哪个模型、
// 谁手上是哪一版」。这里同时锁住三件事：精细记录在、状态码是 409、
// 不再重复一条 generic。
func TestUpsertAuditTrail(t *testing.T) {
	setupDB(t)
	if err := model.LOG_DB.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("建审计表失败: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/api/mcp/profiles/by-model", apiUpsertProfile)

	call := func(query, body string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/mcp/profiles/by-model?"+query, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w.Code
	}

	if code := call("model_id=m-audit", `{"kind":"image","display_name":"a"}`); code != http.StatusOK {
		t.Fatalf("首次写入应当 200，实际 %d", code)
	}
	if code := call("model_id=m-audit&expected_version=0", `{"display_name":"b"}`); code != http.StatusConflict {
		t.Fatalf("冲突应当 409，实际 %d", code)
	}

	var rows []struct {
		Action  string
		Status  int
		Success bool
		Other   string
	}
	if err := model.LOG_DB.Raw(
		"SELECT action, status, success, other FROM audit_logs WHERE action LIKE 'mcp.profile%' ORDER BY id asc",
	).Scan(&rows).Error; err != nil {
		t.Fatalf("读审计失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应当只有两条精细审计（创建 + 冲突），实际 %d", len(rows))
	}

	created, conflict := rows[0], rows[1]
	if created.Action != auditActionProfileCreate || created.Status != http.StatusOK || !created.Success {
		t.Errorf("创建审计不对: action=%s status=%d success=%v", created.Action, created.Status, created.Success)
	}
	for _, want := range []string{`"model_id":"m-audit"`, `"via":"upsert"`, `"version":1`} {
		if !strings.Contains(created.Other, want) {
			t.Errorf("创建审计缺 %s: %s", want, created.Other)
		}
	}

	if conflict.Action != auditActionProfileConflict || conflict.Status != http.StatusConflict || conflict.Success {
		t.Errorf("冲突审计不对: action=%s status=%d success=%v", conflict.Action, conflict.Status, conflict.Success)
	}
	for _, want := range []string{`"expected_version":0`, `"current_version":1`, `"exists":true`} {
		if !strings.Contains(conflict.Other, want) {
			t.Errorf("冲突审计缺 %s: %s", want, conflict.Other)
		}
	}

	// handler 已经设了 ContextKeyAuditLogged，兜底不该再插一条。
	var generic int64
	if err := model.LOG_DB.Model(&model.AuditLog{}).Where("action = ?", "generic").Count(&generic).Error; err != nil {
		t.Fatal(err)
	}
	if generic != 0 {
		t.Errorf("不该产生 generic 兜底审计，实际 %d 条", generic)
	}
}

// 静默忽略 enabled 等于骗人：agent 传 false，拿到的是 true。
func TestUpsertRejectsEnabledPatch(t *testing.T) {
	setupDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/api/mcp/profiles/by-model", apiUpsertProfile)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut,
		"/api/mcp/profiles/by-model?model_id=m-enabled",
		strings.NewReader(`{"kind":"image","enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("业务错误应当是 200+success:false，实际 %d", w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["success"] != false {
		t.Fatalf("应当拒绝 enabled，实际 %v", out)
	}
	if _, ok := GetProfileByModelId("m-enabled"); ok {
		t.Fatal("被拒的请求不该落库")
	}
}

// 「删掉 agent key 名、只定义到 token」的前提是 token_ref 真有值。
//
// 操作审计此前只有 user_id，一个账号多把 PAT 时分不清是哪把。
func TestControlAuditCarriesTokenRef(t *testing.T) {
	setupDB(t)
	if err := model.LOG_DB.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("建审计表失败: %v", err)
	}
	gin.SetMode(gin.TestMode)

	const ref = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/mcp/profiles/by-model", nil)
	c.Set("access_token_ref", ref)
	c.Set("id", 7)
	c.Set("role", common.RoleAdminUser)
	c.Set("username", "bot")

	recordControlAudit(c, auditActionProfileUpdate,
		"Updated MCP model profile m", map[string]any{"model_id": "m"})

	var got string
	if err := model.LOG_DB.Raw(
		"SELECT token_ref FROM audit_logs ORDER BY id DESC LIMIT 1",
	).Row().Scan(&got); err != nil {
		t.Fatalf("读审计失败: %v", err)
	}
	if got != ref {
		t.Fatalf("审计没定义到 token: %q", got)
	}
}

// 反向：session 请求没有令牌可归属，token_ref 必须留空。
// 回填不能变成「顺手拿上一次的值」——那会让审计把两个人算成一把钥匙。
func TestControlAuditLeavesTokenRefEmptyForSession(t *testing.T) {
	setupDB(t)
	if err := model.LOG_DB.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("建审计表失败: %v", err)
	}
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/mcp/profiles/by-model", nil)
	c.Set("id", 7)
	c.Set("role", common.RoleAdminUser)
	c.Set("username", "admin")

	recordControlAudit(c, auditActionProfileUpdate,
		"Updated MCP model profile m", map[string]any{"model_id": "m"})

	var got string
	if err := model.LOG_DB.Raw(
		"SELECT token_ref FROM audit_logs ORDER BY id DESC LIMIT 1",
	).Row().Scan(&got); err != nil {
		t.Fatalf("读审计失败: %v", err)
	}
	if got != "" {
		t.Fatalf("session 审计不该有 token_ref，实际 %q", got)
	}
}
