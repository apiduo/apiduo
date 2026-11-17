package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
/* APIDUO:async:BEGIN */
	"github.com/QuantumNous/new-api/pkg/endpoint"
/* APIDUO:async:END */
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterCandidateIDs(t *testing.T) {
	alphaSetting := `{"task_plugin_key":"alpha"}`
	betaSetting := `{"task_plugin_key":"beta"}`
	alpha := &Channel{Id: 900001, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Setting: &alphaSetting}
	beta := &Channel{Id: 900002, Type: constant.ChannelTypeTaskPlugin, Status: common.ChannelStatusEnabled, Setting: &betaSetting}
	ordinary := &Channel{Id: 900003, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled}
	kling := &Channel{Id: 900004, Type: constant.ChannelTypeKling, Status: common.ChannelStatusEnabled}
	jimeng := &Channel{Id: 900005, Type: constant.ChannelTypeJimeng, Status: common.ChannelStatusEnabled}
	matchingCustom := &Channel{Id: 900010, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	matchingCustom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-4"},
			}},
		},
	})
	otherCustom := &Channel{Id: 900011, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled}
	otherCustom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/responses",
				Models:       []string{"gpt-4"},
			}},
		},
	})

	pathFilter := dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: "/v1/chat/completions"}
	emptyPathFilter := dto.ChannelFilter{Kind: dto.FilterRequestPath, RequestPath: ""}

	tests := []struct {
		name      string
		ids       []int
		modelName string
		filters   []dto.ChannelFilter
		wantKept  []int
		wantEmpty dto.ChannelFilterKind
	}{
		{
			name:      "identity keeps matching type-59 key",
			ids:       []int{900001, 900002},
			modelName: "shared",
			filters:   identityFilters("alpha", nil),
			wantKept:  []int{900001},
		},
		{
			name:      "identity empty key drops all type-59",
			ids:       []int{900001, 900002},
			modelName: "shared",
			filters:   identityFilters("", nil),
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "identity empty key keeps ordinary channel",
			ids:       []int{900003},
			modelName: "ordinary",
			filters:   identityFilters("", nil),
			wantKept:  []int{900003},
		},
		{
			name:      "identity keeps matching legacy type",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}),
			wantKept:  []int{900004},
		},
		{
			name:      "identity keeps all listed legacy types",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling, constant.ChannelTypeJimeng}),
			wantKept:  []int{900004, 900005},
		},
		{
			name:      "identity keyed with no types drops legacy",
			ids:       []int{900004, 900005},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", nil),
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "identity drops missing cache entry",
			ids:       []int{900004, 999999},
			modelName: "legacy",
			filters:   identityFilters("legacy-alpha", []int{constant.ChannelTypeKling}),
			wantKept:  []int{900004},
		},
		{
			name:      "empty request path is a passthrough including missing ids",
			ids:       []int{900003, 900010, 999999},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{emptyPathFilter},
			wantKept:  []int{900003, 900010, 999999},
		},
		{
			name:      "request path keeps missing cache entry for consistency",
			ids:       []int{900003, 999999},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{900003, 999999},
		},
		{
			name:      "request path keeps matching type-58 and ordinary",
			ids:       []int{900003, 900010, 900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{900003, 900010},
		},
		{
			name:      "request path empties when only unmatched type-58 remains",
			ids:       []int{900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter},
			wantKept:  []int{},
			wantEmpty: dto.FilterRequestPath,
		},
		{
			name:      "intersection attributes empty set to identity after path keeps candidates",
			ids:       []int{900001, 900010},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{pathFilter, identityFilters("missing", nil)[0]},
			wantKept:  []int{},
			wantEmpty: dto.FilterTaskPluginIdentity,
		},
		{
			name:      "intersection attributes empty set to path when path runs first",
			ids:       []int{900011},
			modelName: "gpt-4",
			filters:   []dto.ChannelFilter{identityFilters("", nil)[0], pathFilter},
			wantKept:  []int{},
			wantEmpty: dto.FilterRequestPath,
		},
	}

	channelSyncLock.Lock()
	previous := channelsIDM
	channelsIDM = map[int]*Channel{
		900001: alpha,
		900002: beta,
		900003: ordinary,
		900004: kling,
		900005: jimeng,
		900010: matchingCustom,
		900011: otherCustom,
	}
	t.Cleanup(func() {
		channelsIDM = previous
		channelSyncLock.Unlock()
	})

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			kept, emptiedBy := filterCandidateIDs(testCase.ids, testCase.modelName, testCase.filters)
			if testCase.wantKept == nil {
				assert.Nil(t, kept)
			} else {
				assert.Equal(t, testCase.wantKept, kept)
			}
			assert.Equal(t, testCase.wantEmpty, emptiedBy)
		})
	}
}

func TestChannelSatisfiesFilters(t *testing.T) {
	alphaSetting := `{"task_plugin_key":"alpha"}`
	alpha := &Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin, Setting: &alphaSetting}
	ordinary := &Channel{Id: 2, Type: constant.ChannelTypeOpenAI}
	custom := &Channel{Id: 3, Type: constant.ChannelTypeAdvancedCustom}
	custom.SetOtherSettings(kitdto.ChannelOtherSettings{
		AdvancedCustom: &kitdto.AdvancedCustomConfig{
			Routes: []kitdto.AdvancedCustomRoute{{
				IncomingPath: "/v1/chat/completions",
				Models:       []string{"gpt-4"},
			}},
		},
	})

	ok, kind := ChannelSatisfiesFilters(nil, "gpt-4", nil)
	assert.False(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(alpha, "shared", identityFilters("alpha", nil))
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(alpha, "shared", identityFilters("beta", nil))
	assert.False(t, ok)
	assert.Equal(t, dto.FilterTaskPluginIdentity, kind)

	ok, kind = ChannelSatisfiesFilters(ordinary, "gpt-4", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/chat/completions",
	}})
	require.True(t, ok)
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(custom, "gpt-4", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/responses",
	}})
	assert.False(t, ok)
	assert.Equal(t, dto.FilterRequestPath, kind)
}

// A task plugin channel takes a Responses WebSocket session when the switch
// is on and its plugin's /v1/responses route for the model has no converter,
// the rule an Advanced Custom channel follows.
/* APIDUO:async:BEGIN */
// A built-in asynchronous endpoint has no synchronous implementation. Letting
// a synchronous channel win would execute the image body as whatever that
// channel's task plugin expects — a video job, in the sora mapping's case.
func TestChannelSatisfiesFiltersBuiltInAsyncEndpoint(t *testing.T) {
	async := &Channel{Id: 900101, Type: constant.ChannelTypeAdvancedAsync}
	syncImage := &Channel{Id: 900102, Type: constant.ChannelTypeOpenAI}
	// A vendor channel whose images live on its own endpoint. It is the reason
	// this filter cannot be a list of channel types: type 54 serves images, but
	// nothing about the type says so.
	doubao := &Channel{Id: 900103, Type: constant.ChannelTypeDoubaoVideo}

	asyncFilters := []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: endpoint.AsyncImagePath,
	}}

	ok, kind := ChannelSatisfiesFilters(async, "flux-3", asyncFilters)
	require.True(t, ok, "异步渠道才能服务内置异步端点")
	assert.Equal(t, dto.ChannelFilterKind(""), kind)

	ok, kind = ChannelSatisfiesFilters(syncImage, "flux-3", asyncFilters)
	assert.False(t, ok, "本地执行没开时，同步渠道不能服务异步端点")
	assert.Equal(t, dto.FilterRequestPath, kind)

	// 本地执行打开后，任何渠道都可以接：能不能真的发出一次图像请求，由提交时
	// 构造 URL 判定，不在这里按类型猜。
	previous := apiduoLocalImageAsyncEnabled
	apiduoLocalImageAsyncEnabled = func() bool { return true }
	t.Cleanup(func() { apiduoLocalImageAsyncEnabled = previous })

	for _, ch := range []*Channel{syncImage, doubao} {
		ok, kind = ChannelSatisfiesFilters(ch, "flux-3", asyncFilters)
		assert.True(t, ok, "本地执行开启时渠道 %d 应可接异步端点", ch.Type)
		assert.Equal(t, dto.ChannelFilterKind(""), kind)
	}

	// 同步入口保持原样：同步渠道照常通过。
	ok, _ = ChannelSatisfiesFilters(syncImage, "flux-3", []dto.ChannelFilter{{
		Kind:        dto.FilterRequestPath,
		RequestPath: "/v1/images/generations",
	}})
	require.True(t, ok)
}

/* APIDUO:async:END */
func TestTaskPluginChannelResponsesWebSocket(t *testing.T) {
	for key, relay := range map[string]string{
		"ws-native":    `[{incoming_path: "/v1/responses", upstream_path: "/responses", models: ["m1"]}]`,
		"ws-converted": `[{incoming_path: "/v1/responses", upstream_path: "/responses", converter: "plugin"}]`,
	} {
		source := `export const meta = {apiVersion: 1, key: "` + key + `", name: "WS", version: "1.0.0", author: {name: "Test"}, relay: ` + relay + `};`
		if key == "ws-converted" {
			source += `export function convertRequest(ctx, req) { return {body: req.body}; } export function convertResponse(ctx, res) { return {body: res.body}; } export function convertStreamEvent() { return {}; }`
		}
		_, err := jsplugin.DefaultRegistry.Register(source, jsplugin.Options{})
		require.NoError(t, err)
		t.Cleanup(func() { _ = jsplugin.DefaultRegistry.Unregister(key) })
	}
	channel := func(key string, enabled bool) *Channel {
		ch := &Channel{Id: 1, Type: constant.ChannelTypeTaskPlugin}
		ch.SetSetting(kitdto.ChannelSettings{TaskPluginKey: key, ResponsesWebSocketEnabled: enabled})
		return ch
	}
	filters := []dto.ChannelFilter{{Kind: dto.FilterResponsesWebSocket}}

	ok, _ := ChannelSatisfiesFilters(channel("ws-native", true), "m1", filters)
	assert.True(t, ok)
	ok, kind := ChannelSatisfiesFilters(channel("ws-native", false), "m1", filters)
	assert.False(t, ok, "the channel switch is off")
	assert.Equal(t, dto.FilterResponsesWebSocket, kind)
	ok, _ = ChannelSatisfiesFilters(channel("ws-native", true), "m2", filters)
	assert.False(t, ok, "the route does not serve the model")
	ok, _ = ChannelSatisfiesFilters(channel("ws-converted", true), "m1", filters)
	assert.False(t, ok, "a converted route cannot forward native events")
	ok, _ = ChannelSatisfiesFilters(channel("ws-missing", true), "m1", filters)
	assert.False(t, ok)
}
/* APIDUO:async:BEGIN */

// 排除过滤器：任务重试时要能说「不要再是这一条」。
func TestChannelSatisfiesFiltersExcludeChannels(t *testing.T) {
	first := &Channel{Id: 501}
	second := &Channel{Id: 502}
	filters := []dto.ChannelFilter{{Kind: dto.FilterExcludeChannels, ExcludeChannelIDs: []int{501}}}

	ok, kind := ChannelSatisfiesFilters(first, "m", filters)
	assert.False(t, ok, "已试过的渠道必须被排除")
	assert.Equal(t, dto.FilterExcludeChannels, kind)

	ok, _ = ChannelSatisfiesFilters(second, "m", filters)
	assert.True(t, ok, "没试过的渠道不受影响")

	// 空排除列表等于没配，不能把所有渠道都挡掉。
	ok, _ = ChannelSatisfiesFilters(first, "m", []dto.ChannelFilter{{Kind: dto.FilterExcludeChannels}})
	assert.True(t, ok)
}
/* APIDUO:async:END */
