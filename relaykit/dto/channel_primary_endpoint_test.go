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

func TestImageProtocolFollowsPrimaryOnlyForImageEndpoints(t *testing.T) {
	base := &ImageProtocolConfig{Enabled: true, Upstream: ImageUpstreamOpenAI}

	if got := base.WithPrimaryFallback(" /v1/images/custom ").Primary(); got != "/v1/images/custom" {
		t.Fatalf("image primary must become the primary path, got %q", got)
	}
	if got := base.WithPrimaryFallback("/v1/chat/completions").Primary(); got != "/v1/images/generations" {
		t.Fatalf("non-image primary must keep the default primary, got %q", got)
	}

	explicit := &ImageProtocolConfig{
		Enabled:     true,
		Upstream:    ImageUpstreamOpenAI,
		PrimaryPath: "/v1/images/explicit",
	}
	if got := explicit.WithPrimaryFallback("/v1/images/other").Primary(); got != "/v1/images/explicit" {
		t.Fatalf("explicit primary must win over the primary, got %q", got)
	}

	disabled := &ImageProtocolConfig{Upstream: ImageUpstreamOpenAI}
	if got := disabled.WithPrimaryFallback("/v1/images/custom").Primary(); got != "/v1/images/generations" {
		t.Fatalf("disabled protocol must ignore the primary, got %q", got)
	}
}

func TestImageProtocolServesPath(t *testing.T) {
	enabled := &ImageProtocolConfig{Enabled: true, Upstream: ImageUpstreamARK}
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits", "/v1/images/edits/"} {
		if !enabled.ServesPath(path) {
			t.Fatalf("%q must be served by the image protocol", path)
		}
	}
	if enabled.ServesPath("/v1/chat/completions") {
		t.Fatal("chat must not be claimed by the image protocol")
	}
	if enabled.ServesPath("/v1/images/other") {
		t.Fatal("only the two canonical image entries are claimed")
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
		{Enabled: true, Upstream: ImageUpstreamOpenAI, PrimaryPath: "/v1/images/generations", EditPath: "/v1/images/edits"},
		{Enabled: true, Upstream: ImageUpstreamARK, PrimaryPath: "/v1/images/generations/"},
	}
	for _, cfg := range ok {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("expected %+v to pass, got %v", cfg, err)
		}
	}
	bad := []*ImageProtocolConfig{
		{Enabled: true, Upstream: "bogus"},
		{Enabled: true, Upstream: ImageUpstreamARK, PrimaryPath: "//evil.example/x"},
		{Enabled: true, Upstream: ImageUpstreamARK, PrimaryPath: "/v1/ima ges/x"},
		{Enabled: true, Upstream: ImageUpstreamARK, EditPath: "/v1/images/edits?x=1"},
		{Enabled: true, Upstream: ImageUpstreamARK, EditPath: "/api/advanced-custom/routes"},
	}
	for _, cfg := range bad {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected %+v to be rejected", cfg)
		}
	}
}
