package model

import (
	"slices"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/endpoint"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
)

var filterEvalOrder = []dto.ChannelFilterKind{
	// APIDUO:exclude-channels:BEGIN
	// First: it is a membership test, and running it before the expensive
	// filters keeps a retried task from re-reading a plugin's route table.
	dto.FilterExcludeChannels,
	// APIDUO:exclude-channels:END
	dto.FilterRequestPath,
	dto.FilterTaskPluginEndpoint,
	dto.FilterTaskPluginIdentity,
	dto.FilterResponsesWebSocket,
}

// apiduoLocalImageAsyncEnabled is the local-execution switch, indirected so a
// test can exercise both answers. The real value is read once per process from
// the environment, which makes it untestable in place: the first caller fixes
// it for the whole run, and whether that caller is a test or the filter depends
// on test order.
var apiduoLocalImageAsyncEnabled = common.LocalImageAsyncEnabled

// ChannelSatisfiesFilters reports whether ch passes every filter.
// On false, it returns the first violated filter in filterEvalOrder for error
// attribution.
func ChannelSatisfiesFilters(ch *Channel, modelName string, filters []dto.ChannelFilter) (bool, dto.ChannelFilterKind) {
	if ch == nil {
		return false, ""
	}
	for _, kind := range filterEvalOrder {
		for _, filter := range filters {
			if filter.Kind != kind {
				continue
			}
			if !channelMatchesFilter(ch, modelName, filter) {
				return false, kind
			}
		}
	}
	return true, ""
}

// filterCandidateIDs applies filters to a cached candidate id list.
// Caller must hold channelSyncLock (read lock). The input slice is never mutated.
// A missing id in channelsIDM is kept for request_path (downstream consistency
// error) and dropped for the other filters.
func filterCandidateIDs(ids []int, modelName string, filters []dto.ChannelFilter) (kept []int, emptiedBy dto.ChannelFilterKind) {
	if len(ids) == 0 {
		return ids, ""
	}
	kept = ids
	for _, kind := range filterEvalOrder {
		kindFilters := filtersByKind(filters, kind)
		if len(kindFilters) == 0 {
			continue
		}
		next := make([]int, 0, len(kept))
		for _, id := range kept {
			channel, exists := channelsIDM[id]
			if candidatePassesKindFilters(channel, exists, modelName, kind, kindFilters) {
				next = append(next, id)
			}
		}
		if len(kept) > 0 && len(next) == 0 {
			return next, kind
		}
		kept = next
	}
	return kept, ""
}

func filtersByKind(filters []dto.ChannelFilter, kind dto.ChannelFilterKind) []dto.ChannelFilter {
	var matched []dto.ChannelFilter
	for _, filter := range filters {
		if filter.Kind == kind {
			matched = append(matched, filter)
		}
	}
	return matched
}

func candidatePassesKindFilters(ch *Channel, exists bool, modelName string, kind dto.ChannelFilterKind, filters []dto.ChannelFilter) bool {
	if kind == dto.FilterRequestPath && !exists {
		return true
	}
	if !exists || ch == nil {
		return false
	}
	for _, filter := range filters {
		if !channelMatchesFilter(ch, modelName, filter) {
			return false
		}
	}
	return true
}

func channelMatchesFilter(ch *Channel, modelName string, filter dto.ChannelFilter) bool {
	switch filter.Kind {
	case dto.FilterExcludeChannels:
		// APIDUO:exclude-channels:BEGIN
		return !slices.Contains(filter.ExcludeChannelIDs, ch.Id)
		// APIDUO:exclude-channels:END
	case dto.FilterRequestPath:
		if filter.RequestPath == "" {
			return true
		}
		// APIDUO:async-endpoint-filter:BEGIN
		// A built-in asynchronous endpoint has no synchronous implementation:
		// only an advanced-async channel speaks its protocol. Any other type
		// would take the body it received and execute it as a different
		// request — an image prompt parsed as a video task, for instance.
		//
		// The exception is local execution: when the operator allows it, any
		// channel may take the task, because the gateway does not relay the
		// request — it stores it and replays it against the address that
		// channel publishes for its own images. Which channels can do that is
		// not a question this filter can answer cheaply: it needs the adaptor
		// and the base URL. Listing types here (only OpenAI, say) is the same
		// mistake as hardcoding the upstream path — it silently drops the
		// channels whose images live on a vendor endpoint of their own. The
		// decisive check happens at submit, where the request is built, and a
		// channel that cannot build one rejects the attempt instead of
		// accepting it.
		if _, ok := endpoint.LookupAsync(filter.RequestPath); ok {
			if ch.Type == constant.ChannelTypeAdvancedAsync {
				return true
			}
			// A task-plugin channel is excluded even here: its requests are
			// routed to the plugin before the local executor is reached, so
			// letting it in would answer the same endpoint with a second
			// response shape — the one thing this endpoint exists to prevent.
			return apiduoLocalImageAsyncEnabled() && ch.Type != constant.ChannelTypeTaskPlugin
		}
		// APIDUO:async-endpoint-filter:END
		// APIDUO:advanced-async:BEGIN The task-only type has no sync route table,
		// so it must not take the "not an advanced custom channel" pass.
		if !constant.IsAdvancedCustomChannel(ch.Type) && ch.Type != constant.ChannelTypeAdvancedAsync {
			return true
		}
		// APIDUO:advanced-async:END
		config := ch.GetOtherSettings().AdvancedCustom
		// APIDUO:advanced-async:BEGIN
		if ch.Type == constant.ChannelTypeAdvancedAsync {
			asyncConfig := ch.GetOtherSettings().AdvancedAsync
			if asyncConfig.SupportsIncomingPath(filter.RequestPath) {
				return true
			}
			// 自定义入站端点会在分发之前被改写成任务中继路径，此时过滤器看到的
			// 已经是中继路径。中继路径是网关内部约定，不该要求运营在每条渠道的
			// 入站列表里再声明一遍 —— 漏写的结果是重入之后选不到渠道（503），
			// 比不接管更糟。
			if filter.RequestPath == constant.ApiduoTaskRelayPath {
				return true
			}
		}
		// APIDUO:advanced-async:END
		// APIDUO:primary-endpoint:BEGIN
		// 图片协议自己接管 generations / edits：这两个入口的语义在协议层定死，
		// 不看转发路由表。否则「启用了协议但没写路由」的渠道会被选择器过滤掉，
		// 客户端拿到 503 而协议层其实完全能处理。
		if ch.GetOtherSettings().ImageProtocol.ServesPath(filter.RequestPath) {
			return true
		}
		// APIDUO:primary-endpoint:END
		return config != nil && config.SupportsPathForModel(filter.RequestPath, modelName)
	case dto.FilterTaskPluginIdentity:
		if filter.TaskPluginKey == "" {
			return ch.Type != constant.ChannelTypeTaskPlugin || providerChannelServes(ch, filter.RequestPath, modelName)
		}
		if ch.Type == constant.ChannelTypeTaskPlugin || ch.Type == constant.ChannelTypeNewAPI {
			// A New API channel serves every plugin it is extended with; the
			// pinned plugin or any shared-model candidate may execute there.
			setting := ch.GetSetting()
			return setting.BindsTaskPlugin(filter.TaskPluginKey) || slices.ContainsFunc(filter.TaskPluginKeys, setting.BindsTaskPlugin)
		}
		return slices.Contains(filter.TaskPluginChannelTypes, ch.Type)
	case dto.FilterTaskPluginEndpoint:
		pluginChannel := slices.Contains(filter.TaskPluginChannelTypes, ch.Type)
		if ch.Type == constant.ChannelTypeTaskPlugin || ch.Type == constant.ChannelTypeNewAPI {
			pluginChannel = slices.ContainsFunc(filter.TaskPluginKeys, ch.GetSetting().BindsTaskPlugin)
		}
		relayChannel := ch.Type != constant.ChannelTypeTaskPlugin || providerChannelServes(ch, filter.RequestPath, modelName)
		if filter.TaskPluginRelayOnly {
			return relayChannel && !pluginChannel
		}
		// Ordinary channels and matching plugin channels compete using the same
		// group, priority, weight, affinity, and pin rules. Unrelated type-61
		// plugins cannot execute this endpoint unless their relay routes do.
		return relayChannel || pluginChannel
	case dto.FilterResponsesWebSocket:
		if !ch.GetSetting().ResponsesWebSocketEnabled {
			return false
		}
		switch ch.Type {
		case constant.ChannelTypeOpenAI, constant.ChannelTypeAzure, constant.ChannelTypeCodex, constant.ChannelTypeSub2API, constant.ChannelTypeNewAPI:
			return true
		case constant.ChannelTypeAdvancedCustom:
			// The session forwards native Responses events without protocol
			// conversion, so only a converter-free /v1/responses route qualifies.
			route, ok := ch.GetOtherSettings().AdvancedCustom.MatchPathForModel("/v1/responses", modelName)
			return ok && route.IsNative()
		case constant.ChannelTypeTaskPlugin:
			// The same rule on the relay routes of the channel's plugin.
			plugin, ok := jsplugin.DefaultRegistry.Get(ch.GetSetting().TaskPluginKey)
			return ok && plugin.Meta.ServesResponsesWebSocket(modelName)
		default:
			return false
		}
	default:
		return true
	}
}

// providerChannelServes reports a task plugin channel whose plugin relays
// requestPath for modelName through its relay routes, as an Advanced Custom
// channel with those routes would.
func providerChannelServes(ch *Channel, requestPath, modelName string) bool {
	if ch.Type != constant.ChannelTypeTaskPlugin || requestPath == "" {
		return false
	}
	plugin, ok := jsplugin.DefaultRegistry.Get(ch.GetSetting().TaskPluginKey)
	if !ok || !plugin.Meta.HasProviderPart() {
		return false
	}
	_, ok = plugin.Meta.RelayRoute(requestPath, modelName)
	return ok
}
