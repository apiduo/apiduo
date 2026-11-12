package model

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
)

// Playground 的 /pg 前缀只是拿浏览器会话替代 sk 令牌，与 /v1 语义相同。
// 但分发器给每个请求都挂一条 FilterRequestPath，高级异步渠道（8001）的路由表
// 写的是 /v1 —— 不归一化时整条渠道被过滤掉，线上表现是 503「无可用渠道」，
// 看起来像插件或渠道挂了。
func TestChannelMatchesFilterPlaygroundPathAlias(t *testing.T) {
	setting := `{"advanced_async":{"incoming_paths":["/v1/video/generations"]}}`
	asyncChannel := &Channel{
		Id:      18,
		Type:    constant.ChannelTypeAdvancedAsync,
		Group:   "default",
		Models:  "hailuo-video-sec",
		Setting: &setting,
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		// 修复点：/pg 归一化成 /v1 之后才命中渠道路由表。
		{"playground video matches the v1 inbound entry", "/pg/video/generations", true},
		{"v1 video keeps matching", "/v1/video/generations", true},
		// 8001 是纯异步通道，同步图片入口必须继续被拒 —— 归一化不能把这条
		// 保护一起绕过，否则异步渠道会被同步请求选中然后失败。
		{"sync images stays reserved for the synchronous relay", "/pg/images/generations", false},
		{"undeclared path is still rejected", "/pg/chat/completions", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := ChannelSatisfiesFilters(asyncChannel, "hailuo-video-sec", []dto.ChannelFilter{
				{Kind: dto.FilterRequestPath, RequestPath: tc.path},
			})
			if got != tc.want {
				t.Fatalf("ChannelSatisfiesFilters(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// 普通渠道不看路由表，任何路径都放行 —— 归一化不该改变这一点。
func TestChannelMatchesFilterPlainChannelIgnoresPath(t *testing.T) {
	plain := &Channel{Id: 14, Type: 1, Group: "default", Models: "doubao-seedream-5-0-flash-260915"}
	for _, path := range []string{"/pg/images/generations", "/pg/video/generations", "/v1/chat/completions"} {
		got, _ := ChannelSatisfiesFilters(plain, "doubao-seedream-5-0-flash-260915", []dto.ChannelFilter{
			{Kind: dto.FilterRequestPath, RequestPath: path},
		})
		if !got {
			t.Fatalf("plain channel should pass %q", path)
		}
	}
}
