package mcp

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// 内网地址不该出现在给 agent 的错误里：既无助于排查，又泄露部署结构。
func TestWrapNetErrHidesInternalHost(t *testing.T) {
	c := &upClient{baseURL: "http://127.0.0.1:3000/v1"}
	raw := &url.Error{
		Op:  "Post",
		URL: "http://127.0.0.1:3000/v1/images/generations",
		Err: context.DeadlineExceeded,
	}
	err := c.wrapNetErr(raw, "sk-secret")
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("错误里仍有内网地址: %v", err)
	}
	if strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("错误里仍有 api_key: %v", err)
	}
	if !strings.Contains(err.Error(), "/v1/images/generations") {
		t.Fatalf("路径要保留，否则无法定位是哪个端点: %v", err)
	}
	var ut *errUpstreamTimeout
	if !errors.As(err, &ut) {
		t.Fatalf("超时应包成 errUpstreamTimeout，实际 %T", err)
	}
}

// 超时必须单独归类：这类失败往往是「已经生成并计费、只是没等到响应」，
// 标成可重试会诱导用户再付一次钱。
func TestUpstreamTimeoutIsNotRetryable(t *testing.T) {
	te := classifyToolError("m-qwen", &errUpstreamTimeout{Msg: "请求上游超时: Post \"/v1/images/generations\": context deadline exceeded"})
	if te == nil {
		t.Fatal("应产出结构化错误")
	}
	if te.Code != CodeUpstreamTimeout {
		t.Fatalf("code = %s，期望 %s", te.Code, CodeUpstreamTimeout)
	}
	if te.Retryable {
		t.Fatal("超时不该标可重试（重试可能重复扣费）")
	}
	if te.RetryAction != "check_console" {
		t.Fatalf("retry_action = %q，期望 check_console", te.RetryAction)
	}
	if !strings.Contains(te.Hint, "计费") {
		t.Fatalf("hint 应提醒可能已计费: %q", te.Hint)
	}
}

// 普通网络错误（非超时）保持原有语义，但同样不能带内网地址。
func TestWrapNetErrKeepsGenericMessage(t *testing.T) {
	c := &upClient{baseURL: "https://relay.internal/v1"}
	err := c.wrapNetErr(errors.New("dial tcp 10.0.0.5: connect: connection refused"), "")
	if strings.Contains(err.Error(), "relay.internal") {
		t.Fatalf("不该带内网主机: %v", err)
	}
	if !strings.Contains(err.Error(), "请求上游失败") {
		t.Fatalf("普通网络错误文案被改坏: %v", err)
	}
	var ut *errUpstreamTimeout
	if errors.As(err, &ut) {
		t.Fatal("非超时不该包成超时")
	}
}
