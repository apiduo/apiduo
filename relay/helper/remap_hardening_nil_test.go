package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

func TestRemapHardeningEnabledGuardOrder(t *testing.T) {
	// ChannelMeta is an embedded pointer; a request that failed before channel
	// selection has it nil while IsModelMapped still appears addressable.
	if RemapHardeningEnabled(nil, nil) {
		t.Fatal("nil info must not be enabled")
	}
	if RemapHardeningEnabled(nil, &relaycommon.RelayInfo{}) {
		t.Fatal("missing ChannelMeta without context must not be enabled")
	}
	if RemapHardeningEnabled(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{IsModelMapped: false},
	}) {
		t.Fatal("unmapped request must not be enabled")
	}
	if RemapHardeningEnabled(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{IsModelMapped: true},
	}) {
		t.Fatal("mapping without the channel flag must not be enabled")
	}
	// Errors raised before InitChannelMeta (an exhausted balance, for one) still
	// need the catalog, so the switch alone must enable it.
	if !RemapHardeningEnabled(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{RemapHardening: true},
		},
	}) {
		t.Fatal("channel flag must enable the error catalog without a mapping")
	}
	// The response body, however, only needs rewriting when a mapping ran.
	if RemapResponseRewrite(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{RemapHardening: true},
		},
	}) {
		t.Fatal("response rewrite must require a matched mapping")
	}
	if !RemapResponseRewrite(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			IsModelMapped:        true,
			ChannelOtherSettings: dto.ChannelOtherSettings{RemapHardening: true},
		},
	}) {
		t.Fatal("matched mapping with the flag must rewrite the response")
	}
}

func TestRemapHardeningEnabledFallsBackToContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	// The distributor writes this before any handler runs, which is what makes
	// quota rejections expressible even though InitChannelMeta has not run.
	common.SetContextKey(c, constant.ContextKeyChannelOtherSetting,
		dto.ChannelOtherSettings{RemapHardening: true})

	if !RemapHardeningEnabled(c, &relaycommon.RelayInfo{}) {
		t.Fatal("context fallback must enable the catalog before InitChannelMeta")
	}
	if RemapResponseRewrite(c, &relaycommon.RelayInfo{}) {
		t.Fatal("context fallback alone must not rewrite a response body")
	}
}

func TestRemapModelIdentityGuardOrder(t *testing.T) {
	// Same embedded-pointer trap as RemapHardeningEnabled: a request rejected
	// before channel selection (unpriced model, exhausted balance) has a nil
	// ChannelMeta, and reading IsModelMapped then dereferences it.
	if upstream, public := RemapModelIdentity(nil); upstream != "" || public != "" {
		t.Fatalf("nil info must be empty, got %q/%q", upstream, public)
	}
	if upstream, public := RemapModelIdentity(&relaycommon.RelayInfo{}); upstream != "" || public != "" {
		t.Fatalf("missing ChannelMeta must be empty, got %q/%q", upstream, public)
	}
	if upstream, public := RemapModelIdentity(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{IsModelMapped: false},
	}); upstream != "" || public != "" {
		t.Fatalf("unmapped request must be empty, got %q/%q", upstream, public)
	}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			IsModelMapped:     true,
			UpstreamModelName: "gpt-5.4-mini",
		},
	}
	info.OriginModelName = "gpt-5.4"
	if upstream, public := RemapModelIdentity(info); upstream != "gpt-5.4-mini" || public != "gpt-5.4" {
		t.Fatalf("identity = %q/%q", upstream, public)
	}
}

func TestImageProtocolEnabledSources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if ImageProtocolEnabled(nil, nil) {
		t.Fatal("nil info and nil context must not be enabled")
	}
	if ImageProtocolEnabled(nil, &relaycommon.RelayInfo{}) {
		t.Fatal("missing ChannelMeta without context must not be enabled")
	}
	on := &dto.ImageProtocolConfig{Enabled: true, Upstream: dto.ImageUpstreamGemini}
	off := &dto.ImageProtocolConfig{Enabled: false, Upstream: dto.ImageUpstreamGemini}
	if !ImageProtocolEnabled(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{ImageProtocol: on},
		},
	}) {
		t.Fatal("channel image protocol must be visible on the relay info")
	}
	if ImageProtocolEnabled(nil, &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelOtherSettings: dto.ChannelOtherSettings{ImageProtocol: off},
		},
	}) {
		t.Fatal("a disabled protocol must not enable the envelope")
	}

	// Before InitChannelMeta the distributor's context settings are the only source.
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	common.SetContextKey(c, constant.ContextKeyChannelOtherSetting, dto.ChannelOtherSettings{ImageProtocol: on})
	if !ImageProtocolEnabled(c, nil) {
		t.Fatal("context settings must be read when the relay info is missing")
	}
}
