package model

import (
	"slices"
	// APIDUO:playground-path-alias:BEGIN
	"strings"
	// APIDUO:playground-path-alias:END

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
)

var filterEvalOrder = []dto.ChannelFilterKind{
	dto.FilterRequestPath,
	dto.FilterTaskPluginEndpoint,
	dto.FilterTaskPluginIdentity,
	dto.FilterResponsesWebSocket,
}

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
	case dto.FilterRequestPath:
		if filter.RequestPath == "" {
			return true
		}
		// APIDUO:playground-path-alias:BEGIN
		// Playground 的 /pg 前缀只是拿浏览器会话替代 sk 令牌，语义与 /v1 一致；
		// 而渠道路由表、图片协议、高级自定义路由表里写的都是 /v1。不归一化时
		// 这条过滤器会把「路由表只声明了 /v1」的渠道整条排除，distributor 于是
		// 回 503「无可用渠道」—— 现象看起来像插件或渠道挂了，实际只是前缀不同。
		// 放在这里归一化一次，下游四个判定点全部受益。
		requestPath := filter.RequestPath
		if strings.HasPrefix(requestPath, "/pg/") {
			requestPath = "/v1/" + strings.TrimPrefix(requestPath, "/pg/")
		}
		// APIDUO:playground-path-alias:END
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
			if asyncConfig.SupportsIncomingPath(requestPath) {
				return true
			}
			// 自定义入站端点会在分发之前被改写成任务中继路径，此时过滤器看到的
			// 已经是中继路径。中继路径是网关内部约定，不该要求运营在每条渠道的
			// 入站列表里再声明一遍 —— 漏写的结果是重入之后选不到渠道（503），
			// 比不接管更糟。比较用归一化后的路径：/pg/video/generations 归一化
			// 就是中继路径本身，playground 的视频提交与它语义完全一致。
			if requestPath == constant.ApiduoTaskRelayPath {
				return true
			}
		}
		// APIDUO:advanced-async:END
		// APIDUO:primary-endpoint:BEGIN
		// 图片协议自己接管 generations / edits：这两个入口的语义在协议层定死，
		// 不看转发路由表。否则「启用了协议但没写路由」的渠道会被选择器过滤掉，
		// 客户端拿到 503 而协议层其实完全能处理。
		if ch.GetOtherSettings().ImageProtocol.ServesPath(requestPath) {
			return true
		}
		// APIDUO:primary-endpoint:END
		// APIDUO:playground-path-alias:BEGIN
		// 收尾这一行原本用的是原始 filter.RequestPath，归一化后改用上面的值。
		return config != nil && config.SupportsPathForModel(requestPath, modelName)
		// APIDUO:playground-path-alias:END
	case dto.FilterTaskPluginIdentity:
		if filter.TaskPluginKey == "" {
			return ch.Type != constant.ChannelTypeTaskPlugin
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
		if filter.TaskPluginRelayOnly {
			return ch.Type != constant.ChannelTypeTaskPlugin && !pluginChannel
		}
		// Ordinary channels and matching plugin channels compete using the same
		// group, priority, weight, affinity, and pin rules. Unrelated type-61
		// plugins cannot execute this endpoint.
		return ch.Type != constant.ChannelTypeTaskPlugin || pluginChannel
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
		default:
			return false
		}
	default:
		return true
	}
}
