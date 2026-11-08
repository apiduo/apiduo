package advancedcustom

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

// primary_endpoint 是「主端点 + 派生 + 显式覆盖」三层里的中间层：
// 路由没写上游路径时跟随主端点，写了的保持原样。
func TestResolveFallsBackToChannelPrimary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)

	info := advancedCustomRelayInfo(&dto.AdvancedCustomConfig{
		Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/images/edits"}},
	})
	info.RelayMode = relayconstant.RelayModeImagesEdits
	info.ChannelOtherSettings.PrimaryEndpoint = " /v1/images/generations "

	adaptor := &Adaptor{}
	if err := adaptor.resolve(c, info); err != nil {
		t.Fatalf("resolve must succeed: %v", err)
	}
	if adaptor.route.UpstreamPath != "/v1/images/generations" {
		t.Fatalf("route must follow the primary, got %q", adaptor.route.UpstreamPath)
	}
}

func TestResolveKeepsExplicitRouteUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)

	info := advancedCustomRelayInfo(&dto.AdvancedCustomConfig{
		Routes: []dto.AdvancedCustomRoute{{
			IncomingPath: "/v1/images/edits",
			UpstreamPath: "/v1/images/edits",
		}},
	})
	info.RelayMode = relayconstant.RelayModeImagesEdits
	info.ChannelOtherSettings.PrimaryEndpoint = "/v1/images/generations"

	adaptor := &Adaptor{}
	if err := adaptor.resolve(c, info); err != nil {
		t.Fatalf("resolve must succeed: %v", err)
	}
	if adaptor.route.UpstreamPath != "/v1/images/edits" {
		t.Fatalf("explicit upstream must win over the primary, got %q", adaptor.route.UpstreamPath)
	}
}

func TestResolveRejectsEmptyUpstreamWithoutPrimary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", nil)

	info := advancedCustomRelayInfo(&dto.AdvancedCustomConfig{
		Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/images/edits"}},
	})
	info.RelayMode = relayconstant.RelayModeImagesEdits

	adaptor := &Adaptor{}
	err := adaptor.resolve(c, info)
	if err == nil || !strings.Contains(err.Error(), "upstream_path") {
		t.Fatalf("empty upstream without primary must be rejected at validation, got %v", err)
	}
}

// 图片协议没显式配 primary_path 时，跟随渠道主端点。这是「一个渠道一个母语」
// 在协议层的落点：主端点选到图片端点，chat 传图与双端点就都指向同一个地方。
func TestPrepareImageProtocolFollowsChannelPrimary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/images/generations",
		strings.NewReader(`{"model":"m","prompt":"p"}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	info := apiduoImageTestInfo()
	info.RelayMode = relayconstant.RelayModeImagesGenerations
	info.ChannelOtherSettings.ImageProtocol.PrimaryPath = ""
	info.ChannelOtherSettings.PrimaryEndpoint = "/v1/images/custom-generations"

	adaptor := &Adaptor{}
	plan, handled, err := adaptor.prepareImageProtocol(c, info)
	if !handled || err != nil {
		t.Fatalf("image protocol must handle the request, handled=%v err=%v", handled, err)
	}
	if plan.UpstreamPath != "/v1/images/custom-generations" {
		t.Fatalf("primary path must follow the primary, got %q", plan.UpstreamPath)
	}
}
