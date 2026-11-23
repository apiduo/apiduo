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
