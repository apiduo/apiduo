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
