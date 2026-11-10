package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// 线上实测：给 image.generate 传一个 search 模型、而当时一个 image 模型都没启用时，
// 收到的是「没有已启用的 image 模型，请先在能力注册里添加」—— 调用方会照着去建声明，
// 真正要改的其实是 model 参数。所以点名了模型就先回答「这个模型能不能用」。
func TestPickViewPrefersExplicitModel(t *testing.T) {
	views := []modelView{{ID: "probe", Kind: "search"}, {ID: "pic", Kind: "image"}}

	if _, err := pickView(views, "video", "probe"); err == nil {
		t.Fatal("search 模型不能用于 video，应当报错")
	} else {
		var mismatch *errKindMismatch
		if !errors.As(err, &mismatch) {
			t.Fatalf("应当报类型不匹配，实际 %T: %v", err, err)
		}
		if mismatch.Actual != "search" {
			t.Errorf("应当指出实际类型是 search，实际 %q", mismatch.Actual)
		}
	}

	// 没有 video 模型、也没点名：这条才该说「先去注册」。
	if _, err := pickView(views, "video", ""); err == nil {
		t.Fatal("没有 video 模型时应当报错")
	} else {
		var none errNoModelOfKind
		if !errors.As(err, &none) {
			t.Fatalf("应当报「没有该类型的模型」，实际 %T: %v", err, err)
		}
	}

	// 点名了一个根本不存在的模型，要报「未注册」而不是「类型不对」。
	if _, err := pickView(views, "image", "ghost"); err == nil {
		t.Fatal("不存在的模型应当报错")
	} else {
		var missing *errModelNotAvailable
		if !errors.As(err, &missing) {
			t.Fatalf("应当报未注册，实际 %T: %v", err, err)
		}
	}
}

// 控制面绝不能把驱动原文回给前端：线上实测重复 model_id 时面板红条里出现过
// `Error 1062 (23000): Duplicate entry '…' for key 'mcp_model_profiles.uk_…'`。
func TestDuplicateProfileHasReadableMessage(t *testing.T) {
	setupDB(t)

	p := ModelProfile{ModelId: "dup-model", Kind: "image", Protocol: "openai_image", Enabled: true}
	if err := SaveProfile(&p); err != nil {
		t.Fatalf("第一次创建失败: %v", err)
	}

	again := ModelProfile{ModelId: "dup-model", Kind: "image", Protocol: "openai_image"}
	err := SaveProfile(&again)
	if !errors.Is(err, ErrProfileDuplicate) {
		t.Fatalf("应当返回 ErrProfileDuplicate，实际 %v", err)
	}
	msg := err.Error()
	for _, leak := range []string{"1062", "Duplicate entry", "uk_mcp_profile_model", "mcp_model_profiles"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("文案里出现了库内部信息 %q: %s", leak, msg)
		}
	}
	if !strings.Contains(msg, "已有能力声明") {
		t.Errorf("文案要说清「已经有了」，实际 %q", msg)
	}
}

// 统一出口的策略：用户可见错误原样出，其余一律换成人话并把原文写进服务端日志。
func TestApiWriteErrHidesDatabaseText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	post := func(err error) string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		apiWriteErr(c, "保存能力声明", err)
		var body map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatalf("响应不是 JSON: %v / %s", e, w.Body.String())
		}
		return fmt.Sprint(body["message"])
	}

	dbErr := errors.New("Error 1062 (23000): Duplicate entry 'x' for key 'mcp_model_profiles.uk_mcp_profile_model'")
	if msg := post(dbErr); strings.Contains(msg, "1062") || strings.Contains(msg, "Duplicate entry") {
		t.Fatalf("数据库原文泄露到前端: %s", msg)
	} else if !strings.Contains(msg, "内部错误") {
		t.Errorf("默认分支应当给出可操作提示，实际 %q", msg)
	}

	if msg := post(userErrf("产物类型不合法: %q", "audio")); !strings.Contains(msg, "产物类型不合法") {
		t.Errorf("用户可见错误应原样透出，实际 %q", msg)
	}

	if msg := post(ErrProfileDuplicate); !strings.Contains(msg, "已有能力声明") {
		t.Errorf("哨兵应原样透出，实际 %q", msg)
	}
}

// 本地拒绝也必须留观察样本 —— 它是「声明收得太紧」的唯一信号。
// 修之前这里一条都不落库，面板显示「近 7 天 0 次样本」、巡检还判成 unprobed。
func TestLocalRejectLeavesObservationSample(t *testing.T) {
	setupDB(t)
	mockUpstream(t)
	resetProtocol()

	limit := 1
	p := ModelProfile{
		ModelId: "reject-obs", Kind: "image", Protocol: "openai_image",
		Enabled: true, MaxPromptChars: &limit,
		Parameters: `{"size":{"enum":["1K","2K"]}}`,
	}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate",
		// 提示词 3 个字 > 上限 1，必然在本地被拒；同时带上 size 供快照断言。
		"arguments": map[string]any{"prompt": "一只猫", "size": "1K"},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("超长提示词应当被拒，实际 %v", res)
	}

	var rows []model.Log
	if err := model.LOG_DB.Where("model_name = ?", "reject-obs").Find(&rows).Error; err != nil {
		t.Fatalf("查观察行失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("本地拒应当留 1 条样本，实际 %d 条", len(rows))
	}
	row := rows[0]
	if row.Type != logTypeError {
		t.Errorf("被拒样本应记为错误类型，实际 %d", row.Type)
	}
	if !strings.Contains(row.Other, "param_snapshot") {
		t.Fatalf("样本应当带参数快照，实际 %s", truncate(row.Other, 200))
	}
	if !strings.Contains(row.Other, `"size":"1K"`) {
		t.Errorf("快照应当记下 size，实际 %s", truncate(row.Other, 200))
	}
	if strings.Contains(row.Other, "一只猫") {
		t.Fatalf("提示词不许落盘，实际 %s", truncate(row.Other, 200))
	}
	if row.Quota != 0 {
		t.Errorf("观察行 quota 必须为 0，实际 %d", row.Quota)
	}
}

// 快照只收白名单里的结构参数：模型特有参数原样保留可枚举值，复杂值丢弃。
func TestSnapshotInputKeepsOnlyStructuralFields(t *testing.T) {
	n := 2
	got := snapshotInput(generateInput{
		Prompt: "秘密提示词",
		Size:   "2K",
		N:      &n,
		Extra:  map[string]any{"seed": 42, "nested": map[string]any{"a": 1}},
	}, []string{"u1", "u2"})

	if got["size"] != "2K" || got["n"] != "2" || got["reference_images"] != "2" {
		t.Fatalf("结构参数没记全: %v", got)
	}
	if _, ok := got["nested"]; ok {
		t.Errorf("复杂值不该进快照: %v", got)
	}
	for _, v := range got {
		if strings.Contains(v, "秘密提示词") {
			t.Fatalf("提示词不许进快照: %v", got)
		}
	}
}

// 声明了参考图上限，观察维度里就必须有它 —— 否则这条声明永远没有对照。
func TestParamNamesIncludesReferenceImages(t *testing.T) {
	two := 2
	withLimit := ModelProfile{ModelId: "a", MaxRefImages: &two}
	names := strings.Join(ParamNames(withLimit), ",")
	if !strings.Contains(names, "reference_images") {
		t.Fatalf("声明了上限就该观察参考图张数，实际 %s", names)
	}

	without := strings.Join(ParamNames(ModelProfile{ModelId: "b"}), ",")
	if strings.Contains(without, "reference_images") {
		t.Errorf("没声明上限就不必多查一个空维度，实际 %s", without)
	}
}

// 巡检不能把「声明过的参考图上限」当成「未声明的参数」来报。
func TestAuditKnowsReferenceImagesLimit(t *testing.T) {
	two := 2
	p := ModelProfile{ModelId: "img", MaxRefImages: &two}

	// 3 张被拒（超过声明上限 2）：这是正确拒绝，不该报「未声明的边界」
	issues := Audit(p, []ParamStat{{Param: "reference_images", Value: "3", OK: 0, Fail: 4}})
	for _, i := range issues {
		if strings.Contains(i.Detail, "未声明") {
			t.Fatalf("声明过的上限不该报未声明：%+v", i)
		}
	}

	// 只用到 1 张而声明 2 张：属于「上限可能偏大」，这是有用的提示
	issues = Audit(p, []ParamStat{{Param: "reference_images", Value: "1", OK: 9, Fail: 0}})
	found := false
	for _, i := range issues {
		if i.Level == AuditPending && strings.Contains(i.Object, "reference_images") {
			found = true
		}
	}
	if !found {
		t.Fatalf("声明 2 张只用到 1 张，应当提示上限偏大，实际 %+v", issues)
	}
}

// 前端有下拉框，接口层此前一条都没校验：直连 API 能写出 protocol=grpc 的声明。
// 那条声明看起来保存成功，实际每次工具调用都在 getTransport 处失败。
func TestValidateProfileShapeRejectsUnknownProtocol(t *testing.T) {
	p := ModelProfile{ModelId: "m", Kind: "image", Protocol: "grpc"}
	err := validateProfileShape(&p)
	if err == nil || !strings.Contains(err.Error(), "传输协议") {
		t.Fatalf("未知协议必须在写入时拒掉，实际 %v", err)
	}

	for _, ok := range []string{"openai_image", "openai_video"} {
		p := ModelProfile{ModelId: "m", Kind: "image", Protocol: ok}
		if err := validateProfileShape(&p); err != nil {
			t.Errorf("%s 应当合法，实际 %v", ok, err)
		}
	}
}

// 负数上限在运行时按「不支持参考图」处理，提示还写「上限 0」——
// 数据本身是错的，得在写入时拦。
func TestValidateProfileShapeRejectsNegativeLimits(t *testing.T) {
	neg := -1
	for _, tc := range []struct {
		name string
		p    ModelProfile
	}{
		{"参考图", ModelProfile{ModelId: "m", Protocol: "openai_image", MaxRefImages: &neg}},
		{"参考视频", ModelProfile{ModelId: "m", Protocol: "openai_video", MaxRefVideos: &neg}},
		{"提示词", ModelProfile{ModelId: "m", Protocol: "openai_image", MaxPromptChars: &neg}},
	} {
		if err := validateProfileShape(&tc.p); err == nil || !strings.Contains(err.Error(), "不能为负数") {
			t.Errorf("%s上限为负应被拒，实际 %v", tc.name, err)
		}
	}

	zero := 0
	if err := validateProfileShape(&ModelProfile{ModelId: "m", Protocol: "openai_image", MaxRefImages: &zero}); err != nil {
		t.Errorf("0 是「明确不支持」，必须仍然合法，实际 %v", err)
	}
	if err := validateProfileShape(&ModelProfile{ModelId: "m", Protocol: "openai_image"}); err != nil {
		t.Errorf("留空是「未声明」，必须仍然合法，实际 %v", err)
	}
}

// 长度上限对齐 GORM 的 size：SQLite 不强制，MySQL 会报 1406。
func TestValidateProfileShapeRejectsOverlongText(t *testing.T) {
	p := ModelProfile{
		ModelId:     "m",
		Protocol:    "openai_image",
		DisplayName: strings.Repeat("长", 192),
	}
	err := validateProfileShape(&p)
	if err == nil || !strings.Contains(err.Error(), "显示名") {
		t.Fatalf("192 字的显示名应被拒，实际 %v", err)
	}

	p.DisplayName = strings.Repeat("长", 191)
	if err := validateProfileShape(&p); err != nil {
		t.Errorf("191 字正好等于列宽，不该被拒，实际 %v", err)
	}
}
