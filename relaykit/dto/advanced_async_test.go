package dto

import "testing"

func validAdvancedAsyncConfig() *AdvancedAsyncConfig {
	return &AdvancedAsyncConfig{
		IncomingPaths: []string{"/v1/video/generations"},
		Submit: AdvancedAsyncSubmit{
			UpstreamPath: "/v1/video_generation",
			Auth:         &AdvancedCustomRouteAuth{Type: "header", Name: "Authorization", Value: "Bearer {api_key}"},
		},
		Query: AdvancedAsyncQuery{UpstreamPath: "/v1/query/video_generation"},
		Fields: AdvancedAsyncFields{
			TaskIDPath:    []string{"task_id"},
			StatusPath:    []string{"status"},
			ResultURLPath: []string{"video_url"},
		},
	}
}

func TestAdvancedAsyncValidateAppliesDefaults(t *testing.T) {
	config := validAdvancedAsyncConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if config.Submit.Method != "POST" {
		t.Fatalf("submit method = %q, want POST", config.Submit.Method)
	}
	if config.Submit.ContentType != AdvancedAsyncContentTypeJSON {
		t.Fatalf("content type = %q, want json", config.Submit.ContentType)
	}
	if config.Query.Method != "GET" {
		t.Fatalf("query method = %q, want GET", config.Query.Method)
	}
	if config.Query.TaskIDParam != "task_id" {
		t.Fatalf("task id param = %q, want task_id", config.Query.TaskIDParam)
	}
	if config.Query.TaskIDLocation != AdvancedAsyncTaskIDQuery {
		t.Fatalf("task id location = %q, want query", config.Query.TaskIDLocation)
	}
	if config.Billing.Mode != AdvancedAsyncBillingPerCall {
		t.Fatalf("billing mode = %q, want per_call", config.Billing.Mode)
	}
}

func TestAdvancedAsyncValidateRejectsIncompleteConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*AdvancedAsyncConfig)
	}{
		{"no task id path", func(c *AdvancedAsyncConfig) { c.Fields.TaskIDPath = nil }},
		{"no status path", func(c *AdvancedAsyncConfig) { c.Fields.StatusPath = nil }},
		{"bad status map", func(c *AdvancedAsyncConfig) { c.Fields.StatusMap = map[string]string{"done": "DONE"} }},
		{"bad incoming path", func(c *AdvancedAsyncConfig) { c.IncomingPaths = []string{"v1/video"} }},
		{"bad submit method", func(c *AdvancedAsyncConfig) { c.Submit.Method = "DELETE" }},
		{"bad content type", func(c *AdvancedAsyncConfig) { c.Submit.ContentType = "xml" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config := validAdvancedAsyncConfig()
			testCase.mutate(config)
			if err := config.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAdvancedAsyncPathLocationRequiresPlaceholder(t *testing.T) {
	config := validAdvancedAsyncConfig()
	config.Query.TaskIDLocation = AdvancedAsyncTaskIDPath
	if err := config.Validate(); err == nil {
		t.Fatal("expected error when path location has no {task_id}")
	}
	config.Query.UpstreamPath = "/v1/tasks/{task_id}"
	if err := config.Validate(); err != nil {
		t.Fatalf("validate with placeholder: %v", err)
	}
}

func TestSupportsIncomingPath(t *testing.T) {
	config := validAdvancedAsyncConfig()
	if !config.SupportsIncomingPath("/v1/video/generations") {
		t.Fatal("expected incoming path to match")
	}
	if config.SupportsIncomingPath("/v1/chat/completions") {
		t.Fatal("unexpected match")
	}
	var nilConfig *AdvancedAsyncConfig
	if nilConfig.SupportsIncomingPath("/v1/video/generations") {
		t.Fatal("nil config must not match")
	}
}

func TestSupportsIncomingPathMatchesModelTemplate(t *testing.T) {
	config := validAdvancedAsyncConfig()
	config.IncomingPaths = []string{"/v1beta/models/{model}:generateContent"}
	if !config.SupportsIncomingPath("/v1beta/models/gemini-2.5-flash:generateContent") {
		t.Fatal("expected {model} template to match")
	}
	if !config.SupportsIncomingPath("/v1beta/models/gemini-2.5-flash:streamGenerateContent") {
		t.Fatal("expected stream equivalence")
	}
	if config.SupportsIncomingPath("/v1beta/models/a/b:generateContent") {
		t.Fatal("model segment must not contain a slash")
	}
}

func TestLookupAdvancedAsyncPath(t *testing.T) {
	payload := map[string]any{
		"data": map[string]any{
			"tasks": []any{map[string]any{"id": "abc", "state": "done"}},
		},
	}
	value, ok := LookupAdvancedAsyncPath(payload, []string{"data", "tasks", "0", "id"})
	if !ok || value != "abc" {
		t.Fatalf("lookup = %v, %v", value, ok)
	}
	if _, ok := LookupAdvancedAsyncPath(payload, []string{"data", "tasks", "3", "id"}); ok {
		t.Fatal("out of range index must not resolve")
	}
	if _, ok := LookupAdvancedAsyncPath(payload, []string{"missing"}); ok {
		t.Fatal("missing key must not resolve")
	}
}

func TestNormalizeAdvancedAsyncStatus(t *testing.T) {
	fields := AdvancedAsyncFields{StatusMap: map[string]string{"FINISHED_VENDOR": "SUCCESS"}}
	cases := map[string]string{
		"FINISHED_VENDOR": "SUCCESS",
		"succeeded":       "SUCCESS",
		"running":         "IN_PROGRESS",
		"queued":          "QUEUED",
		"failed":          "FAILURE",
		"something-else":  "UNKNOWN",
		"":                "UNKNOWN",
	}
	for input, want := range cases {
		if got := fields.NormalizeAdvancedAsyncStatus(input); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", input, got, want)
		}
	}
}
