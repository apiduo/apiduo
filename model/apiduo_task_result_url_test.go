package model

import (
	"encoding/json"
	"testing"
)

// 上游把成片地址放在 adaptor 配置没覆盖的位置时，任务仍是 SUCCESS 但前端
// 拿不到地址（卡片只有一行「已成功」）。这里锁住几个真实见过的形状。
func TestApiduoResultURLFromData(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "ark task object",
			raw:  `{"id":"cgt-1","status":"succeeded","content":{"video_url":"https://cdn.example/v.mp4"}}`,
			want: "https://cdn.example/v.mp4",
		},
		{
			name: "flat video_url",
			raw:  `{"video_url":"https://cdn.example/v.mp4"}`,
			want: "https://cdn.example/v.mp4",
		},
		{
			name: "openai image async list",
			raw:  `{"data":[{"url":"https://cdn.example/i.png"}]}`,
			want: "https://cdn.example/i.png",
		},
		{
			name: "data uri stays",
			raw:  `{"video_url":"data:video/mp4;base64,AAAA"}`,
			want: "data:video/mp4;base64,AAAA",
		},
		// 失败回包里 error.message 是文案，不能当播放地址。
		{
			name: "failure envelope has no url",
			raw:  `{"status":"failed","error":{"message":"upstream reported failure"}}`,
			want: "",
		},
		{
			name: "empty object",
			raw:  `{}`,
			want: "",
		},
		{
			name: "invalid json",
			raw:  `not json`,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apiduoResultURLFromData(json.RawMessage(tc.raw)); got != tc.want {
				t.Fatalf("apiduoResultURLFromData(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// PrivateData.ResultURL 优先：轮询已经写好了就不要用兜底覆盖它。
func TestGetResultURLPrefersPrivateData(t *testing.T) {
	task := &Task{
		FailReason:  "unused",
		Data:        json.RawMessage(`{"content":{"video_url":"https://fallback.example/v.mp4"}}`),
		PrivateData: TaskPrivateData{ResultURL: "https://polled.example/v.mp4"},
	}
	if got := task.GetResultURL(); got != "https://polled.example/v.mp4" {
		t.Fatalf("GetResultURL() = %q, want the polled URL", got)
	}
}
