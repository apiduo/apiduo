package common

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// 图片协议渠道的响应由 adaptor 解析重编码，通用改写器必须让路；
// 其余渠道保持原行为（改写器照常介入）。
func TestArtifactsHandledByAdaptor(t *testing.T) {
	cases := []struct {
		name     string
		info     *RelayInfo
		expected bool
	}{
		{name: "nil info", info: nil, expected: false},
		{name: "no channel meta", info: &RelayInfo{}, expected: false},
		{
			name:     "meta without image protocol",
			info:     &RelayInfo{ChannelMeta: &ChannelMeta{}},
			expected: false,
		},
		{
			name: "image protocol declared but disabled",
			info: &RelayInfo{ChannelMeta: &ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
				ImageProtocol: &dto.ImageProtocolConfig{Enabled: false},
			}}},
			expected: false,
		},
		{
			name: "image protocol enabled",
			info: &RelayInfo{ChannelMeta: &ChannelMeta{ChannelOtherSettings: dto.ChannelOtherSettings{
				ImageProtocol: &dto.ImageProtocolConfig{Enabled: true, Upstream: "gemini"},
			}}},
			expected: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.ArtifactsHandledByAdaptor(); got != tc.expected {
				t.Errorf("ArtifactsHandledByAdaptor() = %v, want %v", got, tc.expected)
			}
		})
	}
}
