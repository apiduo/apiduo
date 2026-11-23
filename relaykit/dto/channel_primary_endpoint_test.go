package dto

import "testing"

func TestValidatePrimaryEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "empty is allowed", raw: ""},
		{name: "blank is allowed", raw: "   "},
		{name: "plain path", raw: "/v1/images/generations"},
		{name: "full url rejected", raw: "https://api.example.com/v1/chat/completions", wantErr: true},
		{name: "double slash rejected", raw: "//v1/chat/completions", wantErr: true},
		{name: "no leading slash rejected", raw: "v1/chat/completions", wantErr: true},
		{name: "query rejected", raw: "/v1/models?x=1", wantErr: true},
		{name: "fragment rejected", raw: "/v1/models#top", wantErr: true},
		{name: "whitespace rejected", raw: "/v1/chat completions", wantErr: true},
		{name: "management path rejected", raw: "/api/advanced-custom/models", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePrimaryEndpoint(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q", tc.raw)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.raw, err)
			}
		})
	}
}

func TestResolveRouteUpstream(t *testing.T) {
	settings := &ChannelOtherSettings{PrimaryEndpoint: " /v1/images/generations "}

	if got := settings.ResolveRouteUpstream("/v1/images/edits"); got != "/v1/images/edits" {
		t.Fatalf("explicit route upstream must win, got %q", got)
	}
	if got := settings.ResolveRouteUpstream("  "); got != "/v1/images/generations" {
		t.Fatalf("blank route upstream must follow the primary, got %q", got)
	}

	empty := &ChannelOtherSettings{}
	if got := empty.ResolveRouteUpstream(""); got != "" {
		t.Fatalf("no primary and no route upstream must stay empty, got %q", got)
	}
	var nilSettings *ChannelOtherSettings
	if got := nilSettings.ResolveRouteUpstream(""); got != "" {
		t.Fatalf("nil settings must be safe, got %q", got)
	}
}

func TestChannelValidatePrimaryEndpointIsNilSafe(t *testing.T) {
	var settings *ChannelOtherSettings
	if err := settings.ValidatePrimaryEndpoint(); err != nil {
		t.Fatalf("nil settings must not fail validation: %v", err)
	}
}

func TestImageProtocolFollowsChannelPrimary(t *testing.T) {
	base := &ImageProtocolConfig{Enabled: true, Upstream: ImageUpstreamOpenAI}

	if got := base.WithChannelPrimary(" /v1/images/custom ").Primary(); got != "/v1/images/custom" {
		t.Fatalf("image primary must become the primary path, got %q", got)
	}
	if got := base.WithChannelPrimary("/v1/chat/completions").Primary(); got != DefaultImagePrimaryPath {
		t.Fatalf("non-image primary must fall back to the default, got %q", got)
	}
	// 没有渠道主端点（老配置只写了 primary_path）时不留兼容：走默认落点。
	if got := base.WithChannelPrimary("").Primary(); got != DefaultImagePrimaryPath {
		t.Fatalf("missing channel primary must use the default, got %q", got)
	}
	// 关闭时不吃主端点，保持默认。
	disabled := &ImageProtocolConfig{Upstream: ImageUpstreamOpenAI}
	if got := disabled.WithChannelPrimary("/v1/images/custom").Primary(); got != DefaultImagePrimaryPath {
		t.Fatalf("disabled protocol must ignore the primary, got %q", got)
	}
}

func TestImageProtocolServesPath(t *testing.T) {
	enabled := &ImageProtocolConfig{Enabled: true, Upstream: ImageUpstreamARK}
	// 五个入口全部默认接管：开了图片协议的渠道就是图片渠道，不需要在
	// advanced_routes 里逐条声明，也不需要按 body 判断这次要不要出图。
	for _, path := range []string{
		"/v1/images/generations", "/v1/images/edits", "/v1/images/edits/",
		"/v1/chat/completions", "/v1/responses",
		"/v1beta/models/gemini-2.5-flash-image:generateContent",
		"/v1beta/models/google/gemini-nano-banana-2.1:generateContent",
		"/v1beta/models/gemini-2.5-flash-image:streamGenerateContent",
	} {
		if !enabled.ServesPath(path) {
			t.Fatalf("%q must be served by the image protocol", path)
		}
	}
	// 同前缀但不是出图入口，不能顺手接管。
	for _, path := range []string{
		"/v1/images/other",
		"/v1beta/models",
		"/v1beta/models/gemini-2.5-flash-image",
		"/v1/embeddings",
	} {
		if enabled.ServesPath(path) {
			t.Fatalf("%q must not be claimed by the image protocol", path)
		}
	}

	disabled := &ImageProtocolConfig{Upstream: ImageUpstreamARK}
	if disabled.ServesPath("/v1/images/generations") {
		t.Fatal("a disabled protocol must claim nothing")
	}

	var nilConfig *ImageProtocolConfig
	if nilConfig.ServesPath("/v1/images/generations") {
		t.Fatal("nil config must claim nothing")
	}
}

func TestNormalizePrimaryEndpointTrimsTrailingSlash(t *testing.T) {
	cases := map[string]string{
		" /v1/images/generations/ ": "/v1/images/generations",
		"/v1/images/generations//":  "/v1/images/generations",
		"/":                         "/",
		"//":                        "//",
		"":                          "",
		"   ":                       "",
	}
	for raw, want := range cases {
		if got := NormalizePrimaryEndpoint(raw); got != want {
			t.Fatalf("NormalizePrimaryEndpoint(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestValidatePrimaryEndpointAcceptsTrailingSlash(t *testing.T) {
	if err := ValidatePrimaryEndpoint("/v1/images/generations/"); err != nil {
		t.Fatalf("trailing slash must be normalized away, got %v", err)
	}
	if err := ValidatePrimaryEndpoint("//"); err == nil {
		t.Fatal("double slash must not be normalized into an empty declaration")
	}
}

func TestImageProtocolValidateMatchesPrimaryEndpointShape(t *testing.T) {
	ok := []*ImageProtocolConfig{
		{Enabled: true, Upstream: ImageUpstreamARK},
		{Enabled: true, Upstream: ImageUpstreamOpenAI, EditPath: "/v1/images/edits"},
	}
	for _, cfg := range ok {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected %+v to pass, got %v", cfg, err)
		}
	}
	bad := []*ImageProtocolConfig{
		{Enabled: true, Upstream: "bogus"},
		{Enabled: true, Upstream: ImageUpstreamARK, EditPath: "//evil.example/x"},
		{Enabled: true, Upstream: ImageUpstreamARK, EditPath: "/v1/ima ges/x"},
		{Enabled: true, Upstream: ImageUpstreamARK, EditPath: "/v1/images/edits?x=1"},
		{Enabled: true, Upstream: ImageUpstreamARK, EditPath: "/api/advanced-custom/routes"},
	}
	for _, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected %+v to be rejected", cfg)
		}
	}
}

// 0.0.70 及更早把 image_protocol.upstream 存成型号名（seedream / gpt-image-2）。
// 改名为协议名（ark / openai）时没随附数据迁移，老渠道 settings 里仍是旧值；
// 读取侧不收敛的话，发版后这些渠道的每次图片请求都会在 Validate 上 400，
// 而报错发生在请求期，运维看不出是配置问题。
func TestNormalizeImageUpstream(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"seedream", ImageUpstreamARK},
		{"gpt-image-2", ImageUpstreamOpenAI},
		{"ark", ImageUpstreamARK},
		{"openai", ImageUpstreamOpenAI},
		{" ARK ", ImageUpstreamARK},
		{"", ""},
		{"something-else", "something-else"},
	}
	for _, c := range cases {
		if got := NormalizeImageUpstream(c.raw); got != c.want {
			t.Errorf("NormalizeImageUpstream(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestValidateImageProtocolAcceptsLegacyUpstream(t *testing.T) {
	for _, legacy := range []string{"seedream", "gpt-image-2"} {
		cfg := &ImageProtocolConfig{Enabled: true, Upstream: legacy}
		if err := cfg.Validate(); err != nil {
			t.Errorf("legacy upstream %q rejected: %v", legacy, err)
		}
	}
}
