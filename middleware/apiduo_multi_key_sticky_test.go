package middleware

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

func newStickyMiddlewareChannel(keys []string) *model.Channel {
	channel := &model.Channel{Key: strings.Join(keys, "\n")}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = len(keys)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	return channel
}

func newStickyTestContext(tokenID int) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	if tokenID > 0 {
		c.Set("token_id", tokenID)
	}
	return c
}

func TestOverrideStickyChannelKeyLeavesUpstreamResultForOtherModes(t *testing.T) {
	channel := newStickyMiddlewareChannel([]string{"a", "b"})
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling

	key, index, apiErr := overrideStickyChannelKey(newStickyTestContext(1), channel, "upstream-key", 3, nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if key != "upstream-key" || index != 3 {
		t.Fatalf("non-sticky channel must keep the rotation result, got %q index %d", key, index)
	}
}

func TestOverrideStickyChannelKeyIsStablePerToken(t *testing.T) {
	channel := newStickyMiddlewareChannel([]string{"a", "b", "c", "d"})

	firstKey, firstIndex, apiErr := overrideStickyChannelKey(newStickyTestContext(11), channel, "", 0, nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	otherKey, otherIndex, apiErr := overrideStickyChannelKey(newStickyTestContext(11), channel, "", 1, nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if firstKey != otherKey || firstIndex != otherIndex {
		t.Fatalf("same token must own one key: %q/%d vs %q/%d", firstKey, firstIndex, otherKey, otherIndex)
	}
}

func TestOverrideStickyChannelKeyMovesOnRetry(t *testing.T) {
	channel := newStickyMiddlewareChannel([]string{"a", "b", "c", "d"})
	c := newStickyTestContext(23)

	_, firstIndex, apiErr := overrideStickyChannelKey(c, channel, "", 0, nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	_, secondIndex, apiErr := overrideStickyChannelKey(c, channel, "", 1, nil)
	if apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}
	if firstIndex == secondIndex {
		t.Fatalf("retry inside one request must switch key, stayed at %d", firstIndex)
	}
}

func TestOverrideStickyChannelKeyKeepsUpstreamError(t *testing.T) {
	channel := newStickyMiddlewareChannel([]string{"a", "b"})
	upstreamErr := types.NewError(errors.New("no keys"), types.ErrorCodeChannelNoAvailableKey)

	key, index, apiErr := overrideStickyChannelKey(newStickyTestContext(5), channel, "orig", 2, upstreamErr)
	if apiErr != upstreamErr {
		t.Fatalf("sticky must not swallow the upstream error, got %v", apiErr)
	}
	if key != "orig" || index != 2 {
		t.Fatalf("failed rotation must be passed through, got %q index %d", key, index)
	}
}

func TestOverrideStickyChannelKeyScopesTriedIndexesPerChannel(t *testing.T) {
	first := newStickyMiddlewareChannel([]string{"a", "b", "c", "d"})
	first.Id = 1
	second := newStickyMiddlewareChannel([]string{"a", "b", "c", "d"})
	second.Id = 2

	c := newStickyTestContext(31)
	_, firstIndex, _ := overrideStickyChannelKey(c, first, "", 0, nil)
	_, retryIndex, _ := overrideStickyChannelKey(c, first, "", 1, nil)
	if firstIndex == retryIndex {
		t.Fatal("retry on the same channel must switch key")
	}
	if _, index, apiErr := overrideStickyChannelKey(c, second, "", 0, nil); apiErr != nil || index < 0 || index >= 4 {
		t.Fatalf("second channel must start from a fresh tried set, got index %d err %v", index, apiErr)
	}
}

func TestOverrideStickyChannelKeyConcurrentRetriesGetDistinctKeys(t *testing.T) {
	// A websocket connection reuses one gin context across frames, so two key
	// selections can overlap. Choosing and recording must stay one atomic step,
	// otherwise both pick the same index and the retry collision this feature
	// exists to avoid reappears.
	channel := newStickyMiddlewareChannel([]string{"a", "b", "c", "d"})
	c := newStickyTestContext(41)

	const attempts = 4
	results := make([]int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, index, apiErr := overrideStickyChannelKey(c, channel, "", 0, nil)
			if apiErr != nil {
				results[i] = -1
				return
			}
			results[i] = index
		}(i)
	}
	wg.Wait()

	seen := make(map[int]bool, attempts)
	for _, index := range results {
		if index < 0 {
			t.Fatalf("concurrent attempt errored: %v", results)
		}
		if seen[index] {
			t.Fatalf("two concurrent attempts got index %d: %v", index, results)
		}
		seen[index] = true
	}
}

func TestOverrideStickyChannelKeyKeepsWorkingAfterExhaustingKeys(t *testing.T) {
	channel := newStickyMiddlewareChannel([]string{"a", "b"})
	c := newStickyTestContext(53)
	for attempt := range 5 {
		key, index, apiErr := overrideStickyChannelKey(c, channel, "", 0, nil)
		if apiErr != nil {
			t.Fatalf("attempt %d failed: %v", attempt, apiErr)
		}
		if key == "" || index < 0 || index > 1 {
			t.Fatalf("attempt %d returned %q index %d", attempt, key, index)
		}
	}
}

func TestSetupContextForSelectedChannelAppliesSticky(t *testing.T) {
	keys := []string{"k1", "k2", "k3", "k4"}
	channel := newStickyMiddlewareChannel(keys)
	channel.Id = 77

	c := newStickyTestContext(91)
	if apiErr := SetupContextForSelectedChannel(c, channel, "gpt-4o"); apiErr != nil {
		t.Fatalf("unexpected error: %v", apiErr)
	}

	key := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
	index := common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	if index < 0 || index >= len(keys) {
		t.Fatalf("index out of range: %d", index)
	}
	if key != keys[index] {
		t.Fatalf("context key %q does not belong to index %d", key, index)
	}
	if !common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
		t.Fatal("multi-key flag must be recorded for logging and key disabling")
	}

	// The same request retrying must land on a different key.
	if apiErr := SetupContextForSelectedChannel(c, channel, "gpt-4o"); apiErr != nil {
		t.Fatalf("unexpected error on retry: %v", apiErr)
	}
	retryIndex := common.GetContextKeyInt(c, constant.ContextKeyChannelMultiKeyIndex)
	if retryIndex == index {
		t.Fatalf("retry stayed on key index %d", index)
	}
}
