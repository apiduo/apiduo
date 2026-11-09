package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
