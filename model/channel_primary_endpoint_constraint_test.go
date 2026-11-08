package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
)

// 图片协议自己接管 generations / edits：路由表里没写这两个入口，渠道也算支持，
// 否则「启用协议但没配路由」的渠道会被选择器过滤掉，客户端拿到无法解释的 503。
func TestChannelMatchesFilterHonoursImageProtocol(t *testing.T) {
	channel := &Channel{
		Type: constant.ChannelTypeAdvancedCustom,
		OtherSettings: `{
			"primary_endpoint": "/v1/images/custom-gen",
			"image_protocol": {"enabled": true, "upstream": "seedream"},
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
	if channelMatchesFilter(channel, "m", filter("/v1/chat/completions")) {
		t.Fatal("chat must still require a declared route")
	}
}
