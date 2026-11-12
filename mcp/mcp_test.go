package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func withTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	return db
}

// 建表必须由本包自己完成，不能依赖 model/main.go 的 AutoMigrate ——
// 那是既有文件，改一处就多一个 sync 冲突点。
func TestMigrateCreatesTablesAndSeedsRules(t *testing.T) {
	db := withTestDB(t)

	if err := Migrate(); err != nil {
		t.Fatalf("Migrate 失败: %v", err)
	}
	if !db.Migrator().HasTable(&ModelProfile{}) {
		t.Fatal("mcp_model_profiles 没建出来")
	}
	if !db.Migrator().HasTable(&ErrorRule{}) {
		t.Fatal("mcp_error_rules 没建出来")
	}

	rules, err := ListErrorRules("")
	if err != nil {
		t.Fatalf("读规则失败: %v", err)
	}
	if len(rules) != len(globalErrorRules) {
		t.Fatalf("全局规则数 = %d，期望 %d", len(rules), len(globalErrorRules))
	}

	// 幂等：再跑一次不能重复插入
	if err := Migrate(); err != nil {
		t.Fatalf("二次 Migrate 失败: %v", err)
	}
	again, _ := ListErrorRules("")
	if len(again) != len(rules) {
		t.Fatalf("Migrate 不幂等：%d → %d", len(rules), len(again))
	}
}

// 三态是本项目最容易写错的地方：nil / 0 / N 必须是三件不同的事。
func TestProfileTriState(t *testing.T) {
	withTestDB(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}

	zero := 0
	p := ModelProfile{ModelId: "m-zero", Kind: "image", MaxRefImages: &zero, Enabled: true}
	if err := SaveProfile(&p); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, ok := GetProfileByModelId("m-zero")
	if !ok || got.MaxRefImages == nil || *got.MaxRefImages != 0 {
		t.Fatalf("0 应当被保留为「明确不支持」，得到 %+v", got.MaxRefImages)
	}

	ten := 10
	q := ModelProfile{ModelId: "m-ten", Kind: "image", MaxRefImages: &ten, Enabled: true}
	if err := SaveProfile(&q); err != nil {
		t.Fatal(err)
	}

	r := ModelProfile{ModelId: "m-unset", Kind: "video", Enabled: true}
	if err := SaveProfile(&r); err != nil {
		t.Fatal(err)
	}
	unset, _ := GetProfileByModelId("m-unset")
	if unset.MaxRefImages != nil {
		t.Fatal("未声明必须落成 NULL，不能是 0")
	}
	// 未声明时 ModelCard 不能输出该键，否则 agent 会以为「上限 0」
	card := ModelCard(unset)
	if input, ok := card["input"].(map[string]any); ok {
		if _, exists := input["max_reference_images"]; exists {
			t.Fatal("未声明不该出现在 model_card 里")
		}
	}
}

// 改 id 等同于换模型，必须拒绝 —— 否则唯一索引会和历史声明打架。
func TestSaveProfileRejectsModelIdChange(t *testing.T) {
	withTestDB(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	p := ModelProfile{ModelId: "keep-me", Kind: "image", Enabled: true}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	p.ModelId = "changed"
	p.DisplayName = "改名"
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	got, err := GetProfile(p.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelId != "keep-me" {
		t.Fatalf("ModelId 被改成了 %q", got.ModelId)
	}
	if got.DisplayName != "改名" {
		t.Fatal("其他字段应当正常更新")
	}
}

// 旧的自带面板已下线：后台只保留前端「系统设置 → 扩展 → MCP」那一套。
// 它重构后协议选项与库里的值对不上，留着就是会写坏数据的入口。
func TestLegacyPanelIsGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Register(r)

	for _, path := range []string{"/mcp/admin", "/mcp/admin/"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s 应已下线，得到 %d", path, w.Code)
		}
	}
}

// 上游 4xx 常把 key 原样回显在错误里（"Incorrect API key provided: sk-xxx"）。
// 这条错误会进 agent 的上下文，所以必须脱敏 —— 而且不能只脱敏调用方自己的 key：
// 代理场景下回显的往往是**渠道** key，MCP 根本没见过它。
func TestRedactHidesUpstreamEchoedCredential(t *testing.T) {
	cases := []struct {
		name string
		in   string
		key  string
		want string
	}{
		{"调用方 key 被精确替换", "bad key: user-key-123456", "user-key-123456", "bad key: ***"},
		{"渠道 key 按形态脱敏", "Incorrect API key provided: sk-channel-abcdef123456", "user-key",
			"Incorrect API key provided: sk-***"},
		{"空 key 也扫形态", "Incorrect API key provided: sk-channel-abcdef123456", "",
			"Incorrect API key provided: sk-***"},
		{"不含密钥的文本原样返回", "model not found: gpt-4o-mini", "user-key",
			"model not found: gpt-4o-mini"},
		{"短串不误伤", "see sk-learn docs", "", "see sk-learn docs"},
	}
	for _, c := range cases {
		if got := redact(c.in, c.key); got != c.want {
			t.Errorf("%s: redact(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// 保存声明不能顺手改启停。bool 的零值是 false，任何漏传 enabled 的调用方
// 都会把模型静默停用 —— agent 那边直接看不到它，界面却显示「保存成功」。
func TestSaveProfileKeepsEnabled(t *testing.T) {
	withTestDB(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	p := ModelProfile{ModelId: "keep-on", Kind: "image", Enabled: true}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}

	// 模拟只改显示名、enabled 留零值的客户端
	edit := ModelProfile{Id: p.Id, ModelId: "keep-on", Kind: "image", DisplayName: "只改名"}
	if err := SaveProfile(&edit); err != nil {
		t.Fatal(err)
	}
	got, err := GetProfile(p.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatal("保存声明把模型停用了：漏传 enabled 就按 false 落库")
	}
	if got.DisplayName != "只改名" {
		t.Fatalf("其他字段应正常更新，实际 %q", got.DisplayName)
	}

	// 启停只走独立入口
	if err := SetProfileEnabled(p.Id, false); err != nil {
		t.Fatal(err)
	}
	got, _ = GetProfile(p.Id)
	if got.Enabled {
		t.Fatal("SetProfileEnabled(false) 没生效")
	}
	if err := SetProfileEnabled(p.Id, true); err != nil {
		t.Fatal(err)
	}
	got, _ = GetProfile(p.Id)
	if !got.Enabled {
		t.Fatal("SetProfileEnabled(true) 没生效")
	}
	if err := SetProfileEnabled(999999, true); err == nil {
		t.Fatal("不存在的 id 应当报错，而不是静默成功")
	}
}

// rawPatch 把 JSON 文本解成「键 → 原始片段」，模拟 API 层收到的请求体。
func rawPatch(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("patch 不是合法 JSON: %v", err)
	}
	return m
}

// 只改显示名的请求不能顺手把异步模型改成同步、也不能把模型停用。
// 这是「PUT 全量 + bool 零值」这个组合最典型的翻车方式，且界面显示「保存成功」。
func TestUpdateProfileOnlyWritesGivenKeys(t *testing.T) {
	withTestDB(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	refs := 2
	p := ModelProfile{ModelId: "patch-me", Kind: "video", Protocol: "ark",
		Async: true, Enabled: true, MaxRefImages: &refs, Summary: "旧摘要"}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}

	got, err := UpdateProfile(p.Id, rawPatch(t, `{"display_name":"新名字"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "新名字" {
		t.Fatalf("display_name 没更新，实际 %q", got.DisplayName)
	}
	if !got.Async {
		t.Fatal("只改显示名把 async 改成了 false：异步模型会变成同步直返")
	}
	if !got.Enabled {
		t.Fatal("只改显示名把模型停用了")
	}
	if got.MaxRefImages == nil || *got.MaxRefImages != 2 {
		t.Fatalf("未提供的 max_ref_images 被覆盖了：%v", got.MaxRefImages)
	}
	if got.Summary != "旧摘要" {
		t.Fatalf("未提供的 summary 被覆盖了：%q", got.Summary)
	}
	if got.ModelId != "patch-me" || got.Kind != "video" || got.Protocol != "ark" {
		t.Fatalf("业务键被改动：%+v", got)
	}
}

// 显式传 null 是「清空成未声明」，与「没传」必须区分开 ——
// 三态里的 nil 就是靠这条路径写进去的。
func TestUpdateProfileExplicitNullClearsField(t *testing.T) {
	withTestDB(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	refs := 2
	p := ModelProfile{ModelId: "clear-me", Kind: "image", Enabled: true, MaxRefImages: &refs}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	got, err := UpdateProfile(p.Id, rawPatch(t, `{"max_ref_images":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxRefImages != nil {
		t.Fatalf("显式 null 应清成未声明，实际 %v", *got.MaxRefImages)
	}

	// 0 与 null 是两件事：0 是「明确不支持」
	got, err = UpdateProfile(p.Id, rawPatch(t, `{"max_ref_images":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxRefImages == nil || *got.MaxRefImages != 0 {
		t.Fatalf("0 应保留成「明确不支持」，实际 %v", got.MaxRefImages)
	}
}

// model_id 与 enabled 不在可写集合里：前者改名等于换模型，后者走启停接口。
func TestUpdateProfileIgnoresProtectedKeys(t *testing.T) {
	withTestDB(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	p := ModelProfile{ModelId: "protected", Kind: "image", Enabled: true}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	got, err := UpdateProfile(p.Id, rawPatch(t,
		`{"model_id":"hijacked","enabled":false,"bogus_field":1,"display_name":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelId != "protected" {
		t.Fatalf("model_id 被改了：%q", got.ModelId)
	}
	if !got.Enabled {
		t.Fatal("enabled 被这个接口改了：启停应当只走 SetProfileEnabled")
	}
	if got.DisplayName != "ok" {
		t.Fatalf("合法字段应正常写入，实际 %q", got.DisplayName)
	}

	if _, err := UpdateProfile(p.Id, rawPatch(t, `{}`)); err == nil {
		t.Fatal("空 patch 应报错，而不是静默成功")
	}
}

// 规则同理：只改提示语不能把一条「可重试」规则变成「不可重试」。
func TestUpdateErrorRuleOnlyWritesGivenKeys(t *testing.T) {
	withTestDB(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	rules, err := ListErrorRules("")
	if err != nil {
		t.Fatal(err)
	}
	var target ErrorRule
	for _, r := range rules {
		if r.Match == "429" {
			target = r
		}
	}
	if target.Id == 0 {
		t.Fatal("默认规则里应有 429")
	}

	got, err := UpdateErrorRule(target.Id, rawPatch(t, `{"hint":"新的提示"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Hint != "新的提示" {
		t.Fatalf("hint 没更新：%q", got.Hint)
	}
	if !got.Retryable || got.RetryAction != target.RetryAction || got.RetryAfterMs != target.RetryAfterMs {
		t.Fatalf("只改 hint 影响了重试语义：%+v", got)
	}
	if !got.Enabled {
		t.Fatal("只改 hint 把规则禁用了")
	}

	// 全局规则的 match 是识别依据，不可改
	got, err = UpdateErrorRule(target.Id, rawPatch(t, `{"match":"被篡改","retry_after_ms":1234}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Match != "429" {
		t.Fatalf("全局规则 match 被改了：%q", got.Match)
	}
	if got.RetryAfterMs != 1234 {
		t.Fatalf("同一条 patch 里的合法字段应生效，实际 %d", got.RetryAfterMs)
	}
}
