package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// ApiduoAdvancedAsyncServes reports whether an enabled advanced-async channel
// declares this model.
//
// This single condition lets such a channel outrank a task plugin: the alias
// router rewrites the request onto /v1/video/generations before distribution,
// so the channel only has to serve that relay path -- the requested model is
// what decides ownership here.
//
// 缓存开着时只读内存，关掉时读一次表（见 apiduoAsyncCandidateChannels）。
// 判 false 的后果是把同名模型判给插件、渠道变成不可达，没有任何错误可查 ——
// 不能因为一个默认关闭的开关而静默降级。
func ApiduoAdvancedAsyncServes(modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return false
	}
	for _, channel := range apiduoAsyncCandidateChannels() {
		if !apiduoAsyncChannelOpen(channel) {
			continue
		}
		if channelDeclaresModel(channel.Models, modelName) {
			return true
		}
	}
	return false
}

// ApiduoAsyncIncomingServes reports which built-in protocol serves a client
// path for this model, and whether any channel claims it at all.
//
// The path is the one the channel lists in advanced_async.incoming_paths:
// those paths have no static route, so the dispatcher only takes a request
// over when a channel really advertises it. The returned protocol decides the
// client shape of the reply (Images response or task object).
//
// 同一路径被多条渠道声明、协议又不一致时取 images：它的响应形状最窄
// （标准 Images 响应），拿视频任务对象去填只会得到客户端读不懂的 JSON。
// 缓存关闭时回退到数据库：功能不能因为一个默认关闭的开关而静默失效 ——
// 运营配了 incoming_paths 却拿到前端 HTML，没有任何错误提示可查。
// 这条路径每次任务提交只走一次，一次查询的代价远低于不可诊断的故障。
func ApiduoAsyncIncomingServes(requestPath string, modelName string) (string, bool) {
	requestPath = strings.TrimSpace(requestPath)
	modelName = strings.TrimSpace(modelName)
	if requestPath == "" || modelName == "" {
		return "", false
	}
	found := ""
	for _, channel := range apiduoAsyncCandidateChannels() {
		if !apiduoAsyncChannelServes(channel, requestPath) {
			continue
		}
		if !channelDeclaresModel(channel.Models, modelName) {
			continue
		}
		protocol := channel.GetOtherSettings().AdvancedAsync.NormalizedProtocol()
		if protocol == dto.AdvancedAsyncProtocolImages {
			return protocol, true
		}
		found = protocol
	}
	return found, found != ""
}

// ApiduoAsyncIncomingProtocol answers the poll half. The task id is not tied to
// a channel yet at that point, so the path alone decides which response shape
// the client asked for.
func ApiduoAsyncIncomingProtocol(requestPath string) (string, bool) {
	requestPath = strings.TrimSpace(requestPath)
	if requestPath == "" {
		return "", false
	}
	found := ""
	for _, channel := range apiduoAsyncCandidateChannels() {
		if !apiduoAsyncChannelServes(channel, requestPath) {
			continue
		}
		protocol := channel.GetOtherSettings().AdvancedAsync.NormalizedProtocol()
		if protocol == dto.AdvancedAsyncProtocolImages {
			return protocol, true
		}
		found = protocol
	}
	return found, found != ""
}

// apiduoAsyncCandidateChannels returns the channels an incoming async path may
// belong to.
//
// The in-memory index is only built when MEMORY_CACHE_ENABLED is on, so a
// deployment that keeps the switch off would otherwise answer "no channel
// claims this path" for every request and silently drop the traffic onto the
// web fallback. Reading the table instead keeps the feature working in every
// deployment; the query runs once per submission, not per request.
func apiduoAsyncCandidateChannels() []*Channel {
	channelSyncLock.RLock()
	if len(channelsIDM) > 0 {
		channels := make([]*Channel, 0, len(channelsIDM))
		for _, channel := range channelsIDM {
			channels = append(channels, channel)
		}
		channelSyncLock.RUnlock()
		return channels
	}
	channelSyncLock.RUnlock()
	if DB == nil {
		return nil
	}
	var channels []*Channel
	if err := DB.Where("type = ?", constant.ChannelTypeAdvancedAsync).Find(&channels).Error; err != nil {
		return nil
	}
	return channels
}

// apiduoAsyncChannelServes is the shared half of the two lookups above.
func apiduoAsyncChannelServes(channel *Channel, requestPath string) bool {
	if !apiduoAsyncChannelOpen(channel) {
		return false
	}
	return channel.GetOtherSettings().AdvancedAsync.SupportsIncomingPath(requestPath)
}

// apiduoAsyncChannelOpen reports whether the channel is an enabled
// advanced-async channel with its config filled in. Every lookup in this file
// starts here; the path and model checks are added by each caller.
func apiduoAsyncChannelOpen(channel *Channel) bool {
	if channel == nil || channel.Status != common.ChannelStatusEnabled {
		return false
	}
	if channel.Type != constant.ChannelTypeAdvancedAsync {
		return false
	}
	return channel.GetOtherSettings().AdvancedAsync.IsEnabled()
}

// channelDeclaresModel matches the comma-separated model list a channel stores.
// "*" is the wildcard an advanced-async channel uses to accept any model name.
func channelDeclaresModel(models string, modelName string) bool {
	for candidate := range strings.SplitSeq(models, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if candidate == "*" || strings.EqualFold(candidate, modelName) {
			return true
		}
	}
	return false
}

// ApiduoAsyncModelProtocol reports the protocol of the enabled advanced-async
// channel that declares this model.
//
// 提交链上只拿得到模型（入站路径已经被重写成中继路径），所以协议判定只能
// 按模型走。多条渠道声明同一模型时 ark 优先：方舟形状的回复最窄（任务
// 对象），拿 Images 响应去填只会得到客户端读不懂的 JSON。
// 同样在缓存关闭时回落到数据库：判空会让 callback_url 拒绝守卫变成
// fail-open，客户端带着回调地址提交也不会被拦。
func ApiduoAsyncModelProtocol(modelName string) (string, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", false
	}
	found := ""
	for _, channel := range apiduoAsyncCandidateChannels() {
		if !apiduoAsyncChannelOpen(channel) {
			continue
		}
		if !channelDeclaresModel(channel.Models, modelName) {
			continue
		}
		protocol := channel.GetOtherSettings().AdvancedAsync.NormalizedProtocol()
		if protocol == dto.AdvancedAsyncProtocolArk {
			return protocol, true
		}
		found = protocol
	}
	return found, found != ""
}
