package advancedcustomasync

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
)

func testConfig() *kitdto.AdvancedAsyncConfig {
	return &kitdto.AdvancedAsyncConfig{
		IncomingPaths: []string{"/v1/video/generations"},
		Submit: kitdto.AdvancedAsyncSubmit{
			UpstreamPath: "/v1/{model}/submit",
			Auth:         &kitdto.AdvancedCustomRouteAuth{Type: "header", Name: "Authorization", Value: "Bearer {api_key}"},
		},
		Query: kitdto.AdvancedAsyncQuery{
			UpstreamPath:   "/v1/tasks/{task_id}",
			TaskIDLocation: kitdto.AdvancedAsyncTaskIDPath,
		},
		Fields: kitdto.AdvancedAsyncFields{
			TaskIDPath:    []string{"data", "task", "id"},
			StatusPath:    []string{"data", "task", "state"},
			ResultURLPath: []string{"data", "task", "video_url"},
			ErrorPath:     []string{"data", "task", "error"},
			StatusMap:     map[string]string{"DONE_VENDOR": "SUCCESS"},
		},
	}
}

func testAdaptor(t *testing.T, config *kitdto.AdvancedAsyncConfig) *Adaptor {
	t.Helper()
	if err := config.Validate(); err != nil {
		t.Fatalf("config validate: %v", err)
	}
	adaptor := &Adaptor{}
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelBaseUrl:       "https://vendor.example.com",
		ApiKey:               "sk-test",
		UpstreamModelName:    "vendor-model",
		ChannelOtherSettings: kitdto.ChannelOtherSettings{AdvancedAsync: config},
	}})
	return adaptor
}

func TestBuildRequestURLJoinsBaseAndFillsModel(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	info := &relaycommon.RelayInfo{ChannelMeta: adaptor.info.ChannelMeta}

	got, err := adaptor.BuildRequestURL(info)
	if err != nil {
		t.Fatalf("build url: %v", err)
	}
	want := "https://vendor.example.com/v1/vendor-model/submit"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestBuildRequestURLKeepsAbsoluteUpstream(t *testing.T) {
	config := testConfig()
	config.Submit.UpstreamPath = "https://other.example.net/custom/submit"
	adaptor := testAdaptor(t, config)

	got, err := adaptor.BuildRequestURL(adaptor.info)
	if err != nil {
		t.Fatalf("build url: %v", err)
	}
	if got != "https://other.example.net/custom/submit" {
		t.Fatalf("url = %q", got)
	}
}

func TestSubmitQueryAuthAddsConfiguredParameter(t *testing.T) {
	config := testConfig()
	config.Submit.Auth = &kitdto.AdvancedCustomRouteAuth{Type: "query", Name: "key", Value: "{api_key}"}
	adaptor := testAdaptor(t, config)

	got, err := adaptor.BuildRequestURL(adaptor.info)
	if err != nil {
		t.Fatalf("build url: %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if parsed.Query().Get("key") != "sk-test" {
		t.Fatalf("query auth missing, url = %q", got)
	}
}

func TestParseResponseExtractsTaskID(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	body := `{"data":{"task":{"id":"upstream-42","state":"QUEUED"}}}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}

	parsed, taskErr := adaptor.ParseResponse(nil, resp, adaptor.info)
	if taskErr != nil {
		t.Fatalf("parse response: %s", taskErr.Message)
	}
	if parsed.UpstreamTaskID != "upstream-42" {
		t.Fatalf("task id = %q", parsed.UpstreamTaskID)
	}
	if string(parsed.TaskData) != body {
		t.Fatalf("task data = %q", string(parsed.TaskData))
	}
}

func TestParseResponseRejectsMissingTaskID(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{}}`))}

	if _, taskErr := adaptor.ParseResponse(nil, resp, adaptor.info); taskErr == nil {
		t.Fatal("expected missing task id error")
	}
}

func TestParseTaskResultMapsStatusAndResult(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	task := &model.Task{ChannelId: 7}
	task.PrivateData.UpstreamTaskID = "upstream-42"

	body := []byte(`{"data":{"task":{"state":"DONE_VENDOR","video_url":"https://cdn.example.com/a.mp4"}}}`)
	result, err := adaptor.ParseTaskResult(task, nil, body)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Status != "SUCCESS" {
		t.Fatalf("status = %q, want SUCCESS", result.Status)
	}
	if result.Url != "https://cdn.example.com/a.mp4" {
		t.Fatalf("url = %q", result.Url)
	}
}

func TestParseTaskResultCarriesFailureReason(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	task := &model.Task{ChannelId: 7}
	task.PrivateData.UpstreamTaskID = "upstream-42"

	body := []byte(`{"data":{"task":{"state":"failed","error":"quota exceeded"}}}`)
	result, err := adaptor.ParseTaskResult(task, nil, body)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Status != "FAILURE" {
		t.Fatalf("status = %q, want FAILURE", result.Status)
	}
	if result.Reason != "quota exceeded" {
		t.Fatalf("reason = %q", result.Reason)
	}
}

func TestParseTaskResultKeepsUnknownStatusNonTerminal(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	task := &model.Task{ChannelId: 7}
	task.PrivateData.UpstreamTaskID = "upstream-42"

	result, err := adaptor.ParseTaskResult(task, nil, []byte(`{"data":{"task":{"state":"warping"}}}`))
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Status != "UNKNOWN" {
		t.Fatalf("status = %q, want UNKNOWN", result.Status)
	}
}

func TestParseTaskResultLeavesUnsetOptionalFieldsEmpty(t *testing.T) {
	config := testConfig()
	config.Fields.ResultURLPath = nil
	config.Fields.ProgressPath = nil
	config.Fields.ErrorPath = nil
	adaptor := testAdaptor(t, config)
	task := &model.Task{ChannelId: 7}
	task.PrivateData.UpstreamTaskID = "upstream-42"

	body := []byte(`{"data":{"task":{"state":"DONE_VENDOR"}},"noise":"x"}`)
	result, err := adaptor.ParseTaskResult(task, nil, body)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Url != "" || result.Progress != "" || result.Reason != "" {
		t.Fatalf("an empty path must not surface the whole reply: url=%q progress=%q reason=%q",
			result.Url, result.Progress, result.Reason)
	}
}

func TestFetchTaskUsesPathPlaceholder(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	task := &model.Task{ChannelId: 7}
	task.PrivateData.UpstreamTaskID = "upstream 42"

	target, err := buildQueryURL("https://vendor.example.com", adaptor.config.Query, task.GetUpstreamTaskID())
	if err != nil {
		t.Fatalf("build query url: %v", err)
	}
	if !strings.Contains(target, "/v1/tasks/upstream%2042") {
		t.Fatalf("query url = %q", target)
	}
}

func TestEstimateBillingScalesWithRequestedSeconds(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{Mode: kitdto.AdvancedAsyncBillingPerSecond}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"vendor-model","duration":5}`)

	ratios := adaptor.EstimateBilling(nil, nil)
	if ratios["seconds"] != 5 {
		t.Fatalf("ratios = %v, want seconds=5", ratios)
	}
}

func TestEstimateBillingPerCallAddsNoRatio(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	adaptor.submitBody = []byte(`{"model":"vendor-model","duration":5}`)

	if ratios := adaptor.EstimateBilling(nil, nil); ratios != nil {
		t.Fatalf("per_call must not add ratios: %v", ratios)
	}
}

func TestEstimateBillingFallsBackToDefaultSeconds(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:           kitdto.AdvancedAsyncBillingPerSecond,
		DefaultSeconds: 3,
	}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"vendor-model"}`)

	ratios := adaptor.EstimateBilling(nil, nil)
	if ratios["seconds"] != 3 {
		t.Fatalf("ratios = %v, want seconds=3", ratios)
	}
}

func TestParseTaskResultExtractsTokenCounts(t *testing.T) {
	config := testConfig()
	config.Fields.CompletionTokensPath = []string{"usage", "completion_tokens"}
	config.Fields.TotalTokensPath = []string{"usage", "total_tokens"}
	adaptor := testAdaptor(t, config)
	task := &model.Task{ChannelId: 7}
	task.PrivateData.UpstreamTaskID = "upstream-42"

	body := []byte(`{"data":{"task":{"state":"DONE_VENDOR"}},"usage":{"completion_tokens":100000,"total_tokens":120000}}`)
	result, err := adaptor.ParseTaskResult(task, nil, body)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.CompletionTokens != 100000 || result.TotalTokens != 120000 {
		t.Fatalf("tokens = (%d, %d), want (100000, 120000)", result.CompletionTokens, result.TotalTokens)
	}
}

func TestParseTaskResultSkipsUnsetTokenPaths(t *testing.T) {
	adaptor := testAdaptor(t, testConfig())
	task := &model.Task{ChannelId: 7}
	task.PrivateData.UpstreamTaskID = "upstream-42"

	body := []byte(`{"data":{"task":{"state":"DONE_VENDOR"}},"usage":{"completion_tokens":100000}}`)
	result, err := adaptor.ParseTaskResult(task, nil, body)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.CompletionTokens != 0 || result.TotalTokens != 0 {
		t.Fatalf("tokens = (%d, %d), want (0, 0)", result.CompletionTokens, result.TotalTokens)
	}
}

func TestEstimateBillingUsesPerModelMode(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:   kitdto.AdvancedAsyncBillingPerCall,
		Models: map[string]string{"video-per-second": kitdto.AdvancedAsyncBillingPerSecond},
	}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"video-per-second","duration":5}`)

	perSecond := &relaycommon.RelayInfo{
		ChannelMeta:     adaptor.info.ChannelMeta,
		OriginModelName: "video-per-second",
	}
	if ratios := adaptor.EstimateBilling(nil, perSecond); ratios["seconds"] != 5 {
		t.Fatalf("per-second model ratios = %v, want seconds=5", ratios)
	}

	perCall := &relaycommon.RelayInfo{
		ChannelMeta:     adaptor.info.ChannelMeta,
		OriginModelName: "video-per-call",
	}
	if ratios := adaptor.EstimateBilling(nil, perCall); ratios != nil {
		t.Fatalf("per-call model must not add ratios: %v", ratios)
	}
}

func TestAdjustBillingOnCompleteFallsBackToPerCallPrice(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:               kitdto.AdvancedAsyncBillingPerToken,
		TokenFallbackPrice: 0.5,
	}
	adaptor := testAdaptor(t, config)
	task := &model.Task{ChannelId: 7, Group: "default"}
	task.PrivateData.BillingContext = &model.TaskBillingContext{OriginModelName: "vendor-model", GroupRatio: 1}

	if got := adaptor.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{Status: "SUCCESS"}); got != 250000 {
		t.Fatalf("fallback quota = %d, want 250000", got)
	}
	if got := adaptor.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{TotalTokens: 100000}); got != 0 {
		t.Fatalf("a task with tokens must return 0, got %d", got)
	}

	config.Billing.Mode = kitdto.AdvancedAsyncBillingPerCall
	perCall := testAdaptor(t, config)
	if got := perCall.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{}); got != 0 {
		t.Fatalf("per-call mode must return 0, got %d", got)
	}
}

// 时长不一定躺在顶层：方舟把 seconds 塞在参数对象里，配置必须能指到嵌套键。
func TestEstimateBillingReadsNestedDurationPath(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:         kitdto.AdvancedAsyncBillingPerSecond,
		DurationPath: []string{"parameters", "duration"},
	}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"vendor-model","parameters":{"duration":"7"}}`)

	if ratios := adaptor.EstimateBilling(nil, nil); ratios["seconds"] != 7 {
		t.Fatalf("ratios = %v, want seconds=7", ratios)
	}
}

// 读不出秒数又没有兜底价时不能瞎猜：预扣退回按次价（不附加 ratio）。
func TestEstimateBillingIgnoresUnreadableDuration(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{Mode: kitdto.AdvancedAsyncBillingPerSecond}

	for _, body := range []string{
		`{"model":"vendor-model"}`,
		`{"model":"vendor-model","duration":"five"}`,
		`{"model":"vendor-model","duration":0}`,
		`{"model":"vendor-model","duration":-3}`,
	} {
		adaptor := testAdaptor(t, config)
		adaptor.submitBody = []byte(body)
		if ratios := adaptor.EstimateBilling(nil, nil); ratios != nil {
			t.Fatalf("%s produced ratios %v, want nil", body, ratios)
		}
	}
}

// per_token 的预扣来自 ratio 估算，适配器自己不能再加一份 ratio，否则双计。
func TestEstimateBillingPerTokenAddsNoRatio(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{Mode: kitdto.AdvancedAsyncBillingPerToken}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"vendor-model","duration":5}`)

	info := &relaycommon.RelayInfo{ChannelMeta: adaptor.info.ChannelMeta, OriginModelName: "vendor-model"}
	if ratios := adaptor.EstimateBilling(nil, info); ratios != nil {
		t.Fatalf("per_token must not add ratios: %v", ratios)
	}
}

// 轮询重建适配器时 BillingContext 可能已经不在（老任务），模型名要能从任务属性里捞到，
// 否则 per_token 模式识别不出来，兜底价永远不会生效。
func TestAdjustBillingOnCompleteFallsBackToTaskProperties(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:               kitdto.AdvancedAsyncBillingPerCall,
		Models:             map[string]string{"vendor-model": kitdto.AdvancedAsyncBillingPerToken},
		TokenFallbackPrice: 0.5,
	}
	adaptor := testAdaptor(t, config)

	task := &model.Task{ChannelId: 7, Group: "default"}
	task.Properties.OriginModelName = "vendor-model"

	if got := adaptor.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{Status: "SUCCESS"}); got != 250000 {
		t.Fatalf("quota = %d, want 250000 (0.5 USD at 500k quota/unit)", got)
	}
}
