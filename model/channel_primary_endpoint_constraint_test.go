package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
)

// 图片协议的五个入口全部默认接管：路由表里一条都没写，渠道也算支持，
// 否则「启用协议但没配路由」的渠道会被选择器过滤掉，客户端拿到无法解释的 503。
func TestChannelMatchesFilterHonoursImageProtocol(t *testing.T) {
	channel := &Channel{
		Type: constant.ChannelTypeAdvancedCustom,
		OtherSettings: `{
			"primary_endpoint": "/v1/images/custom-gen",
			"image_protocol": {"enabled": true, "upstream": "ark"},
			"advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/images/edits"}]}
		}`,
	}

	filter := func(path string) dto.ChannelFilter {
		return dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: path}
	}

	if !channelMatchesFilter(channel, "m", filter("/v1/images/generations")) {
		t.Fatal("generations must be served even without an explicit route")
	}
	if !channelMatchesFilter(channel, "m", filter("/v1/images/edits")) {
		t.Fatal("edits route is declared, must be served")
	}
	for _, path := range []string{
		"/v1/chat/completions",
		"/v1/responses",
		"/v1beta/models/gemini-2.5-flash-image:generateContent",
		"/v1beta/models/google/gemini-nano-banana-2.1:streamGenerateContent",
	} {
		if !channelMatchesFilter(channel, "m", filter(path)) {
			t.Fatalf("%s must be served without an explicit route", path)
		}
	}
	// 前缀相同但不是出图入口，不能顺手接管。
	for _, path := range []string{"/v1beta/models", "/v1/embeddings"} {
		if channelMatchesFilter(channel, "m", filter(path)) {
			t.Fatalf("%s must not be claimed by the image protocol", path)
		}
	}
}

// 关了图片协议的渠道退回老语义：只有路由表里写过的入口才算支持。
func TestChannelMatchesFilterWithoutImageProtocol(t *testing.T) {
	channel := &Channel{
		Type: constant.ChannelTypeAdvancedCustom,
		OtherSettings: `{
			"advanced_custom": {"advanced_routes": [{"incoming_path": "/v1/chat/completions"}]}
		}`,
	}
	filter := func(path string) dto.ChannelFilter {
		return dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: path}
	}
	if !channelMatchesFilter(channel, "m", filter("/v1/chat/completions")) {
		t.Fatal("declared route must be served")
	}
	if channelMatchesFilter(channel, "m", filter("/v1/images/generations")) {
		t.Fatal("without the protocol, generations needs a declared route")
	}
}
