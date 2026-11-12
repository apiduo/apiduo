// APIDUO:billing-units:BEGIN
package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logOtherFor writes one task consumption log and returns its other map.
func logOtherFor(t *testing.T, priceData types.PriceData) map[string]any {
	t.Helper()
	truncate(t)
	const userID, channelID = 42, 42
	seedUser(t, userID, 10_000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		OriginModelName: "happyhorse11",
		UsingGroup:      "default",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelID},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{Action: "GENERATE"},
		PriceData:       priceData,
	}
	log := callLogTaskConsumption(t, info, makeTask(userID, channelID, 100, 0, BillingSourceWallet, 0))
	require.NotNil(t, log)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(log.Other, &other))
	return other
}

func TestTaskLogRecordsBilledSeconds(t *testing.T) {
	priceData := types.PriceData{ModelPrice: 0.05, Quota: 125000, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}
	priceData.AddOtherRatio("seconds", 5)

	other := logOtherFor(t, priceData)

	assert.Equal(t, float64(5), other["billed_seconds"])
	assert.NotContains(t, other, "billing_factors", "a lone seconds ratio has no extra factors")
}

func TestTaskLogRecordsBillingFactors(t *testing.T) {
	priceData := types.PriceData{ModelPrice: 0.05, Quota: 166666, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}
	priceData.AddOtherRatio("seconds", 5)
	priceData.AddOtherRatio("resolution", 4.0/3.0)

	other := logOtherFor(t, priceData)

	assert.Equal(t, float64(5), other["billed_seconds"])
	factors, ok := other["billing_factors"].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, 4.0/3.0, factors["resolution"], 1e-9)
	assert.NotContains(t, factors, "seconds", "seconds is reported on its own key")
}

func TestTaskLogOmitsBillingUnitsForPerCall(t *testing.T) {
	// A plain per-call task carries no seconds ratio, so the list keeps
	// rendering it as "Per-call" instead of inventing a duration.
	priceData := types.PriceData{ModelPrice: 0.05, Quota: 25000, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}

	other := logOtherFor(t, priceData)

	assert.NotContains(t, other, "billed_seconds")
	assert.NotContains(t, other, "billing_factors")
}

// APIDUO:billing-units:END
