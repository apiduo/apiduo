package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// ── 测试脚手架 ──────────────────────────────────────────────────────────

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开主库失败: %v", err)
	}
	old := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = old })

	// TokenAuthReadOnly 会先查 Redis 缓存；测试里 RDB 是 nil，
	// 而 RedisEnabled 若为 true 会直接空指针 panic（apiduo 既有行为）。
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })

	logDB, err := gorm.Open(sqlite.Open("file:"+t.Name()+"-log?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开日志库失败: %v", err)
	}
	oldLog := model.LOG_DB
	model.LOG_DB = logDB
	t.Cleanup(func() { model.LOG_DB = oldLog })
	if err := logDB.AutoMigrate(&model.Log{}); err != nil {
		t.Fatalf("建日志表失败: %v", err)
	}

	if err := Migrate(); err != nil {
		t.Fatalf("Migrate 失败: %v", err)
	}
	// 令牌表是 TokenAuthReadOnly 的依赖，测试全链路时要用。
	if err := db.AutoMigrate(&model.Token{}); err != nil {
		t.Fatalf("建令牌表失败: %v", err)
	}
	return db
}

// upstreamRecorder 记录 mock 上游收到的请求体，供断言「发出去的到底是什么」。
type upstreamRecorder struct {
	mu    sync.Mutex
	image map[string]any
	video map[string]any
}

func (r *upstreamRecorder) imagePayload() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.image
}

func (r *upstreamRecorder) videoPayload() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.video
}

// mockUpstream 模拟 apiduo 的 /v1：只实现本次链路要用的三个端点。
func mockUpstream(t *testing.T) *upstreamRecorder {
	t.Helper()
	rec := &upstreamRecorder{}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.mu.Lock()
		rec.image = body
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"created":1,"data":[{"url":"https://example.test/a.png","mime_type":"image/png"}]}`)
	})
	mux.HandleFunc("/v1/video/generations", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.mu.Lock()
		rec.video = body
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task_id":"upstream-task-1","status":"queued"}`)
	})
	mux.HandleFunc("/v1/video/generations/upstream-task-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"task_id":"upstream-task-1","status":"succeeded","url":"https://example.test/v.mp4","format":"mp4"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// 必须在 protocolHandler 首次构造前设置：upClient 只在 sync.Once 里创建一次。
	os.Setenv("MCP_UPSTREAM_BASE_URL", srv.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })

	return rec
}

// resetProtocol 让每个测试都能重建 handler（包级 sync.Once 会跨测试复用）。
func resetProtocol() {
	protoOnce = sync.Once{}
	protoHandler = nil
}

// mcpServer 起一个只挂协议端点的测试服务。鉴权中间件是 apiduo 的既有能力，
// 由 apiduo 自己的测试保证；这里验证的是协议层本身。
func mcpServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(injectBearer(protocolHandler()))
	t.Cleanup(srv.Close)
	return srv
}

// rpc 发一条 JSON-RPC 请求，返回 result 字段。
func rpc(t *testing.T, base, apiKey, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, _ := json.Marshal(body)

	req, _ := http.NewRequest(http.MethodPost, base+"/mcp", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s 请求失败: %v", method, err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	text := buf.String()

	// JSONResponse 模式下是纯 JSON；若服务端选了 SSE 就先剥掉 data: 前缀。
	if strings.HasPrefix(strings.TrimSpace(text), "event:") || strings.Contains(text, "\ndata: ") {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "data: ") {
				text = strings.TrimPrefix(line, "data: ")
			}
		}
	}

	var out struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &out); err != nil {
		t.Fatalf("%s 响应不是 JSON：status=%d body=%s", method, resp.StatusCode, truncate(text, 400))
	}
	if out.Error != nil {
		t.Fatalf("%s 返回错误: %s", method, out.Error.Message)
	}
	return out.Result
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// callTool 调一个工具，返回结构化内容。
func callTool(t *testing.T, base, apiKey, name string, args map[string]any) map[string]any {
	t.Helper()
	res := rpc(t, base, apiKey, "tools/call", map[string]any{"name": name, "arguments": args})

	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("工具 %s 报错: %v", name, res["content"])
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("工具 %s 没有 structuredContent: %v", name, res)
	}
	return sc
}

// waitForLogs 等观察队列落库。
func waitForLogs(t *testing.T, want int) []model.Log {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var rows []model.Log
		if err := model.LOG_DB.Order("id asc").Find(&rows).Error; err == nil && len(rows) >= want {
			return rows
		}
		time.Sleep(30 * time.Millisecond)
	}
	var rows []model.Log
	_ = model.LOG_DB.Order("id asc").Find(&rows).Error
	t.Fatalf("等待 %d 条观察日志超时，实际 %d 条", want, len(rows))
	return nil
}

// ── 端到端 ──────────────────────────────────────────────────────────────

// 完整链路：注册声明 → initialize → tools/list → models.list
// → image.generate → video.generate → task.get → 观察落库。
func TestEndToEndProtocol(t *testing.T) {
	setupDB(t)
	rec := mockUpstream(t)
	resetProtocol()

	img := 2
	image := ModelProfile{
		ModelId: "mock-image", Kind: "image", Protocol: "openai_image",
		Enabled: true, MaxRefImages: &img,
		Parameters: `{"size":{"enum":["1K","2K"],"default":"2K"}}`,
		Summary:    "测试图片模型",
	}
	if err := SaveProfile(&image); err != nil {
		t.Fatalf("保存图片声明失败: %v", err)
	}
	video := ModelProfile{
		ModelId: "mock-video", Kind: "video", Protocol: "openai_video",
		Enabled: true, Async: true,
		Parameters: `{"duration":{"min":5,"max":10}}`,
	}
	if err := SaveProfile(&video); err != nil {
		t.Fatalf("保存视频声明失败: %v", err)
	}

	srv := mcpServer(t)

	// 1) initialize
	init := rpc(t, srv.URL, "sk-test", "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e", "version": "1"},
	})
	info, _ := init["serverInfo"].(map[string]any)
	if info["name"] != "apiduo-mcp" {
		t.Fatalf("serverInfo.name = %v", info["name"])
	}

	// 2) tools/list：四个工具都得在
	tl := rpc(t, srv.URL, "sk-test", "tools/list", nil)
	rawTools, _ := tl["tools"].([]any)
	names := map[string]bool{}
	for _, it := range rawTools {
		if m, ok := it.(map[string]any); ok {
			names[fmt.Sprint(m["name"])] = true
		}
	}
	for _, want := range []string{"models.list", "image.generate", "video.generate", "task.get"} {
		if !names[want] {
			t.Fatalf("工具缺失: %s（实际 %v）", want, names)
		}
	}

	// 3) models.list：只返回已启用的声明
	list := callTool(t, srv.URL, "sk-test", "models.list", map[string]any{})
	models, _ := list["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models.list 返回 %d 个，期望 2", len(models))
	}
	first, _ := models[0].(map[string]any)
	if first["kind"] != "image" || first["id"] != "mock-image" {
		t.Fatalf("第一条应是 mock-image/image，实际 %v", first)
	}
	// 声明了 max_reference_images=2 就必须出现在 input 里
	input, _ := first["input"].(map[string]any)
	if fmt.Sprint(input["max_reference_images"]) != "2" {
		t.Fatalf("input.max_reference_images = %v", input["max_reference_images"])
	}

	// 4) image.generate：同步，返回产物
	gen := callTool(t, srv.URL, "sk-test", "image.generate", map[string]any{
		"prompt": "a cat", "size": "2K",
	})
	if gen["status"] != "completed" {
		t.Fatalf("image.generate status = %v", gen["status"])
	}
	outs, _ := gen["outputs"].([]any)
	if len(outs) != 1 {
		t.Fatalf("image.generate 产物数 = %d", len(outs))
	}

	// 5) video.generate：异步，返回签名 task_id
	vg := callTool(t, srv.URL, "sk-test", "video.generate", map[string]any{
		"prompt": "a dog", "extra": map[string]any{"duration": 5},
	})
	if vg["status"] != "working" {
		t.Fatalf("video.generate status = %v", vg["status"])
	}
	taskID, _ := vg["task_id"].(string)
	if taskID == "" {
		t.Fatal("video.generate 没返回 task_id")
	}

	// 6) task.get：用签名 task_id 查回上游任务
	tg := callTool(t, srv.URL, "sk-test", "task.get", map[string]any{"task_id": taskID})
	if tg["status"] != "completed" {
		t.Fatalf("task.get status = %v", tg["status"])
	}
	touts, _ := tg["outputs"].([]any)
	if len(touts) != 1 {
		t.Fatalf("task.get 产物数 = %d", len(touts))
	}

	// 7) 观察落库：两次生成各一条，参数快照里没有 prompt
	rows := waitForLogs(t, 2)
	for _, row := range rows {
		if strings.Contains(row.Other, "a cat") || strings.Contains(row.Other, "a dog") {
			t.Fatalf("观察日志里出现了提示词内容: %s", row.Other)
		}
		if !strings.Contains(row.Other, "param_snapshot") {
			t.Fatalf("观察日志缺 param_snapshot: %s", row.Other)
		}
		if row.ModelName != "mock-image" && row.ModelName != "mock-video" {
			t.Fatalf("观察日志 model_name = %q", row.ModelName)
		}
	}

	// 8) 发出去的请求体要对得上 apiduo 的 /v1 契约
	imgPayload := rec.imagePayload()
	if got := fmt.Sprint(imgPayload["response_format"]); got != "url" {
		t.Errorf("response_format 应固定为 url，实际 %v", got)
	}
	if got := fmt.Sprint(imgPayload["size"]); got != "2K" {
		t.Errorf("size 没透传，实际 %v", got)
	}
	if _, exists := imgPayload["image"]; exists {
		t.Error("没传参考图时不该出现 image 字段")
	}
	if got := fmt.Sprint(imgPayload["model"]); got != "mock-image" {
		t.Errorf("model = %v", got)
	}

	vidPayload := rec.videoPayload()
	if got := fmt.Sprint(vidPayload["prompt"]); got != "a dog" {
		t.Errorf("video prompt = %v", got)
	}
	if got := fmt.Sprint(vidPayload["duration"]); got != "5" {
		t.Errorf("extra 里的 duration 应合并进请求体，实际 %v", got)
	}
}

// 声明式校验必须真的生效，否则面板里填的 enum 只是一句注释。
func TestParamValidationRejectsOutOfRange(t *testing.T) {
	setupDB(t)
	mockUpstream(t)
	resetProtocol()

	zero := 0
	p := ModelProfile{
		ModelId: "strict-image", Kind: "image", Protocol: "openai_image",
		Enabled: true, MaxRefImages: &zero,
		Parameters: `{"size":{"enum":["1K","2K"]},"n":{"min":1,"max":2}}`,
	}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"enum 外取值", map[string]any{"prompt": "x", "size": "8K"}, "不被模型"},
		{"n 超上限", map[string]any{"prompt": "x", "n": 5}, "不能大于"},
		{"参考图超上限", map[string]any{"prompt": "x", "reference_images": []any{"a"}}, "不支持参考图"},
		{"未声明参数", map[string]any{"prompt": "x", "extra": map[string]any{"seed": 1}}, "未声明参数"},
	}
	for _, c := range cases {
		body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "image.generate", "arguments": c.args}}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer sk-test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		resp.Body.Close()
		if !strings.Contains(buf.String(), c.want) {
			t.Errorf("%s：期望包含 %q，实际 %s", c.name, c.want, truncate(buf.String(), 300))
		}
	}
}

// 未注册的模型不能被调用 —— 否则 agent 会靠猜参数，猜错就是一次计费。
func TestUnregisteredModelRejected(t *testing.T) {
	setupDB(t)
	mockUpstream(t)
	resetProtocol()
	srv := mcpServer(t)

	list := callTool(t, srv.URL, "sk-test", "models.list", map[string]any{})
	if n := list["count"]; fmt.Sprint(n) != "0" {
		t.Fatalf("空注册表应返回 0 个模型，实际 %v", n)
	}
	if note, _ := list["note"].(string); !strings.Contains(note, "未注册") && !strings.Contains(note, "已启用") {
		t.Fatalf("空列表应给出可操作的提示，实际 %q", note)
	}
}

// 自调地址必须跟随 PORT。默认值写死 3000 而容器实际监听别的端口时，
// MCP 会把每个调用打到一个没人监听的地址上 —— 而且错误只是 "connection refused"，
// 排查时很容易怀疑到渠道或上游，而不是这一行。
func TestUpstreamDefaultBaseURL(t *testing.T) {
	os.Unsetenv("MCP_UPSTREAM_BASE_URL")

	os.Setenv("PORT", "3999")
	defer os.Unsetenv("PORT")
	if got := newUpClient().baseURL; got != "http://127.0.0.1:3999/v1" {
		t.Fatalf("默认 base_url = %q", got)
	}

	os.Unsetenv("PORT")
	if got := newUpClient().baseURL; got != "http://127.0.0.1:3000/v1" {
		t.Fatalf("无可变端口时应回落 3000，实际 %q", got)
	}

	os.Setenv("MCP_UPSTREAM_BASE_URL", "http://127.0.0.1:8080/v1/")
	if got := newUpClient().baseURL; got != "http://127.0.0.1:8080/v1" {
		t.Fatalf("显式覆盖 + 去尾斜杠失败: %q", got)
	}
	os.Unsetenv("MCP_UPSTREAM_BASE_URL")
}

// SQLite 是 apiduo 的默认库，且没有 JSON_EXTRACT / JSON_UNQUOTE。
// 观察功能必须在这里也能用 —— 否则「本地起一个试试」会直接看到报错。
func TestObservationsOnSQLite(t *testing.T) {
	setupDB(t)

	insertLog := func(params map[string]any, ok bool) {
		typ := logTypeConsume
		if !ok {
			typ = logTypeError
		}
		raw, _ := json.Marshal(map[string]any{
			"admin_info": map[string]any{"param_snapshot": params, "mcp": true},
		})
		row := model.Log{CreatedAt: time.Now().Unix(), Type: typ, ModelName: "obs-video", Other: string(raw)}
		if err := model.LOG_DB.Create(&row).Error; err != nil {
			t.Fatalf("插入日志失败: %v", err)
		}
	}
	for i := 0; i < 12; i++ {
		insertLog(map[string]any{"duration": "5"}, true)
	}
	for i := 0; i < 8; i++ {
		insertLog(map[string]any{"duration": "10"}, true)
	}
	insertLog(map[string]any{"duration": "12"}, false)
	insertLog(map[string]any{"duration": "15"}, false)

	p := ModelProfile{ModelId: "obs-video", Kind: "video", Protocol: "openai_video", Enabled: true}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}

	stats, err := Observations(p, 7)
	if err != nil {
		t.Fatalf("SQLite 下观察查询失败: %v", err)
	}
	byValue := map[string]ParamStat{}
	for _, s := range stats {
		byValue[s.Param+"="+s.Value] = s
	}
	if s := byValue["duration=5"]; s.OK != 12 || s.Fail != 0 {
		t.Errorf("duration=5 期望 12 成功，实际 %+v", s)
	}
	if s := byValue["duration=10"]; s.OK != 8 {
		t.Errorf("duration=10 期望 8 成功，实际 %+v", s)
	}
	if s := byValue["duration=12"]; s.Fail != 1 {
		t.Errorf("duration=12 期望 1 失败，实际 %+v", s)
	}

	// 巡检要能据此报出「声明 30 但 12 被拒」
	max := 30.0
	p.Parameters = `{"duration":{"max":30}}`
	_ = max
	issues := Audit(p, stats)
	found := false
	for _, i := range issues {
		if i.Level == AuditConflict {
			found = true
		}
	}
	if !found {
		t.Errorf("应报出声明与观察冲突，实际 %+v", issues)
	}
}

// FLUX 系渠道会忽略 response_format=url 直接回 base64。
// 只认 url 的实现会把它误报成「上游未返回图片地址」——这是真图测试才暴露的缺口。
func TestParseOutputsHandlesInlineBase64(t *testing.T) {
	raw := map[string]any{
		"data": []any{
			map[string]any{"b64_json": "iVBORw0KGgo=", "mime_type": "image/png"},
			map[string]any{"url": "https://example.test/a.png"},
		},
	}
	out := parseOutputs(raw)
	if len(out) != 2 {
		t.Fatalf("应解析出 2 个产物，实际 %d", len(out))
	}
	if out[0].B64JSON == "" || out[0].URL != "" {
		t.Fatalf("base64 产物解析错误: %+v", out[0])
	}
	if out[1].URL == "" || out[1].B64JSON != "" {
		t.Fatalf("url 产物解析错误: %+v", out[1])
	}

	// 只有 base64 时不能被判成「没有产物」
	onlyB64 := map[string]any{"data": []any{map[string]any{"b64_json": "iVBORw0KGgo="}}}
	if len(parseOutputs(onlyB64)) != 1 {
		t.Fatal("只有 b64_json 时应视为有产物")
	}
}

// 观察行走的是 logs 表，而线上 logs 是 ReplacingMergeTree(ORDER BY created_at, request_id)。
// request_id 为空时，同一秒内的多行会在后台合并时被折叠成一行 —— 样本静默变少，
// 而且只有在 ClickHouse 上才看得出来，SQLite / MySQL 都不会报错。
func TestObservationRowHasRequestId(t *testing.T) {
	setupDB(t)

	writeObservation(callObservation{
		ModelID: "obs-rid",
		OK:      true,
		Params:  map[string]string{"size": "2K"},
	})

	var row model.Log
	if err := model.LOG_DB.Where("model_name = ?", "obs-rid").First(&row).Error; err != nil {
		t.Fatalf("观察行没写进去: %v", err)
	}
	if row.RequestId == "" {
		t.Fatal("request_id 为空：线上 ReplacingMergeTree 会把同秒观察折叠成一行")
	}
	if row.Quota != 0 {
		t.Errorf("观察行 quota 必须为 0，否则统计与 relay 的计费行重复，实际 %d", row.Quota)
	}
	if row.Type != logTypeConsume {
		t.Errorf("成功样本应为消费类型，实际 %d", row.Type)
	}
}

// apiduo 的生成端点是扁平结构，任务查询端点却把记录包在 {code,message,data} 里。
// 只认扁平形状的后果很具体：task.get 永远返回 working，完成时也取不到产物。
// 单测里 mock 的一直是扁平形状，所以这条只有对着真网关才暴露。
func TestTaskGetReadsWrappedGatewayResponse(t *testing.T) {
	setupDB(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/video/generations/task-done", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":"success","message":"","data":{"task_id":"task-1","status":"SUCCESS","progress":"100%","result_url":"https://example.test/v.mp4","fail_reason":"","data":{"status":"queued"}}}`)
	})
	mux.HandleFunc("/v1/video/generations/task-bad", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":"success","data":{"task_id":"task-2","status":"FAILURE","fail_reason":"content blocked"}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	os.Setenv("MCP_UPSTREAM_BASE_URL", srv.URL+"/v1")
	t.Cleanup(func() { os.Unsetenv("MCP_UPSTREAM_BASE_URL") })
	resetProtocol()

	p := ModelProfile{ModelId: "wrapped-video", Kind: "video", Protocol: "openai_video", Enabled: true, Async: true}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	server := mcpServer(t)
	sign := func(upstream string) string {
		return signTaskID("sk-test", taskPayload{
			Model: "wrapped-video", Upstream: upstream, Kind: "video",
			Transport: "openai_video", IssuedAt: time.Now().Unix(),
		})
	}

	done := callTool(t, server.URL, "sk-test", "task.get", map[string]any{"task_id": sign("task-done")})
	if done["status"] != "completed" {
		t.Fatalf("包装形状里的 SUCCESS 应映射成 completed，实际 %v", done["status"])
	}
	outs, _ := done["outputs"].([]any)
	if len(outs) != 1 {
		t.Fatalf("产物数 = %d，期望 1", len(outs))
	}
	if u := fmt.Sprint(outs[0].(map[string]any)["url"]); u != "https://example.test/v.mp4" {
		t.Fatalf("产物应取自 result_url，实际 %q", u)
	}
	// data.data 是上游原始响应，解包不能解第二层：那会把 queued 当最终状态
	if up, _ := done["upstream"].(map[string]any); up["status"] != "SUCCESS" {
		t.Fatalf("upstream 应是解包后的网关记录，实际 %v", up["status"])
	}

	bad := callTool(t, server.URL, "sk-test", "task.get", map[string]any{"task_id": sign("task-bad")})
	if bad["status"] != "failed" {
		t.Fatalf("FAILURE 应映射成 failed，实际 %v", bad["status"])
	}
	if msg, _ := bad["error"].(string); !strings.Contains(msg, "content blocked") {
		t.Fatalf("失败原因应取自 fail_reason，实际 %q", msg)
	}
}

// 声明了 max_prompt_chars 就必须真的拦。只写在 model_card 里不拦，
// agent 会把它当边界，超长提示词照样打到上游 —— 换一个 400 和一次计费。
func TestPromptLimitEnforced(t *testing.T) {
	setupDB(t)
	rec := mockUpstream(t)
	resetProtocol()

	limit := 12
	p := ModelProfile{ModelId: "short-prompt", Kind: "image", Protocol: "openai_image",
		Enabled: true, MaxPromptChars: &limit}
	if err := SaveProfile(&p); err != nil {
		t.Fatal(err)
	}
	srv := mcpServer(t)

	// 中文按字符算：6 个汉字是 6，不是 18
	callTool(t, srv.URL, "sk-test", "image.generate",
		map[string]any{"prompt": "一二三四五六", "model": "short-prompt"})
	if rec.imagePayload() == nil {
		t.Fatal("未超限的请求应该真的发出去了")
	}

	res := rpc(t, srv.URL, "sk-test", "tools/call", map[string]any{
		"name": "image.generate",
		"arguments": map[string]any{
			"prompt": "一二三四五六七八九十十一十二十三",
			"model":  "short-prompt",
		},
	})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("超长提示词应被拒，实际 %v", res)
	}
	if text := fmt.Sprint(res["content"]); !strings.Contains(text, "上限") {
		t.Fatalf("应报出上限，实际 %s", truncate(text, 200))
	}
}
