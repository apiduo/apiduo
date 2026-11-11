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
// Only the in-memory channel cache is consulted. With MEMORY_CACHE_ENABLED off
// the lookup answers false, which restores the plugin-first behaviour instead
// of adding a database read to every submit.
func ApiduoAdvancedAsyncServes(modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return false
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	for _, channel := range channelsIDM {
		if channel == nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if channel.Type != constant.ChannelTypeAdvancedAsync {
			continue
		}
		if !channel.GetOtherSettings().AdvancedAsync.IsEnabled() {
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
// 只查内存缓存；MEMORY_CACHE_ENABLED 关闭时返回 false，请求留在原有路由上，
// 而不是给每次提交加一次数据库读。
func ApiduoAsyncIncomingServes(requestPath string, modelName string) (string, bool) {
	requestPath = strings.TrimSpace(requestPath)
	modelName = strings.TrimSpace(modelName)
	if requestPath == "" || modelName == "" {
		return "", false
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	found := ""
	for _, channel := range channelsIDM {
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
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	found := ""
	for _, channel := range channelsIDM {
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

// apiduoAsyncChannelServes is the shared half of the two lookups above.
func apiduoAsyncChannelServes(channel *Channel, requestPath string) bool {
	if channel == nil || channel.Status != common.ChannelStatusEnabled {
		return false
	}
	if channel.Type != constant.ChannelTypeAdvancedAsync {
		return false
	}
	config := channel.GetOtherSettings().AdvancedAsync
	if !config.IsEnabled() {
		return false
	}
	return config.SupportsIncomingPath(requestPath)
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
func ApiduoAsyncModelProtocol(modelName string) (string, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", false
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	found := ""
	for _, channel := range channelsIDM {
		if channel == nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if channel.Type != constant.ChannelTypeAdvancedAsync {
			continue
		}
		config := channel.GetOtherSettings().AdvancedAsync
		if !config.IsEnabled() || !channelDeclaresModel(channel.Models, modelName) {
			continue
		}
		protocol := config.NormalizedProtocol()
		if protocol == dto.AdvancedAsyncProtocolArk {
			return protocol, true
		}
		found = protocol
	}
	return found, found != ""
}
