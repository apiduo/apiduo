package controller

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	remapKeyA = "sk-remap-aaaaaaaaaaaaaaaaaaaa"
	remapKeyB = "sk-remap-bbbbbbbbbbbbbbbbbbbb"
	remapKeyC = "sk-remap-cccccccccccccccccccc"
)

func remapChannel(keys string, status map[int]int) *model.Channel {
	channel := &model.Channel{Key: keys}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeyStatusList = status
	return channel
}

func assertStatus(t *testing.T, got map[int]int, want map[int]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("status list size = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for index, status := range want {
		if got[index] != status {
			t.Fatalf("status[%d] = %d, want %d (full: %v)", index, got[index], status, got)
		}
	}
}

// The reported bug: disabling slot 1 (key B) and then reversing the list left the
// marker on slot 1, which now holds key C.
func TestRemapMultiKeyStatusFollowsKeyAcrossReorder(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB+"\n"+remapKeyC,
		map[int]int{1: common.ChannelStatusManuallyDisabled})
	updated := remapChannel(remapKeyB+"\n"+remapKeyC+"\n"+remapKeyA, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	assertStatus(t, updated.ChannelInfo.MultiKeyStatusList, map[int]int{0: common.ChannelStatusManuallyDisabled})
}

func TestRemapMultiKeyStatusFollowsKeyAfterInsertion(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB,
		map[int]int{1: common.ChannelStatusManuallyDisabled})
	updated := remapChannel("sk-remap-newkey\n"+remapKeyA+"\n"+remapKeyB, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	assertStatus(t, updated.ChannelInfo.MultiKeyStatusList, map[int]int{2: common.ChannelStatusManuallyDisabled})
}

// A marker for a key that no longer exists cannot be attached to any visible
// line, so it must disappear rather than land on a neighbour.
func TestRemapMultiKeyStatusDropsMarkerForRemovedKey(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB+"\n"+remapKeyC,
		map[int]int{1: common.ChannelStatusManuallyDisabled})
	updated := remapChannel(remapKeyA+"\n"+remapKeyC, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	assertStatus(t, updated.ChannelInfo.MultiKeyStatusList, map[int]int{})
}

func TestRemapMultiKeyStatusCarriesReasonAndTime(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB,
		map[int]int{0: common.ChannelStatusManuallyDisabled})
	origin.ChannelInfo.MultiKeyDisabledReason = map[int]string{0: "401 from upstream"}
	origin.ChannelInfo.MultiKeyDisabledTime = map[int]int64{0: 1794400000}
	updated := remapChannel(remapKeyB+"\n"+remapKeyA, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	if updated.ChannelInfo.MultiKeyDisabledReason[1] != "401 from upstream" {
		t.Fatalf("reason not moved: %v", updated.ChannelInfo.MultiKeyDisabledReason)
	}
	if updated.ChannelInfo.MultiKeyDisabledTime[1] != 1794400000 {
		t.Fatalf("time not moved: %v", updated.ChannelInfo.MultiKeyDisabledTime)
	}
	if _, stale := updated.ChannelInfo.MultiKeyDisabledReason[0]; stale {
		t.Fatalf("stale reason left at old index: %v", updated.ChannelInfo.MultiKeyDisabledReason)
	}
}

func TestRemapMultiKeyStatusNoopWhenKeyUnchanged(t *testing.T) {
	keys := remapKeyA + "\n" + remapKeyB
	origin := remapChannel(keys, map[int]int{1: common.ChannelStatusManuallyDisabled})
	updated := remapChannel(keys, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	if len(updated.ChannelInfo.MultiKeyStatusList) != 0 {
		t.Fatalf("unchanged key list must keep the stored order untouched: %v",
			updated.ChannelInfo.MultiKeyStatusList)
	}
}

func TestRemapMultiKeyStatusNoopWhenNothingDisabled(t *testing.T) {
	origin := remapChannel(remapKeyA+"\n"+remapKeyB, nil)
	updated := remapChannel(remapKeyB+"\n"+remapKeyA, nil)

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	if updated.ChannelInfo.MultiKeyStatusList != nil {
		t.Fatalf("no markers to remap, got %v", updated.ChannelInfo.MultiKeyStatusList)
	}
}

func TestRemapMultiKeyStatusIgnoresSingleKeyChannels(t *testing.T) {
	origin := &model.Channel{Key: remapKeyA}
	origin.ChannelInfo.MultiKeyStatusList = map[int]int{0: common.ChannelStatusManuallyDisabled}
	updated := &model.Channel{Key: remapKeyB}

	remapMultiKeyStatusAfterKeyEdit(origin, updated)

	if updated.ChannelInfo.MultiKeyStatusList != nil {
		t.Fatalf("single-key channel must not gain a status list: %v", updated.ChannelInfo.MultiKeyStatusList)
	}
}

func TestMultiKeyPreviewKeepsHeadAndTail(t *testing.T) {
	// Two real JWT-shaped keys share the same header; only the tail differs.
	got := multiKeyPreview("eyJhbGciOiJIUzI1NiJ9.payload.tfLQLNs")
	if got != "eyJhbGciOi…fLQLNs" {
		t.Fatalf("preview = %q", got)
	}
	other := multiKeyPreview("eyJhbGciOiJIUzI1NiJ9.payload.cS3g530")
	if got == other {
		t.Fatalf("two keys produced the same preview: %q", got)
	}
}

// A self-hosted service key is `sk-` plus its subdomain: eight to twelve
// characters, all of them shorter than the 10+6 window. Returning "" there left
// the panel with rows labelled only `#1 #2 #3`, so they are masked instead of
// dropped.
func TestMultiKeyPreviewMasksShortKeys(t *testing.T) {
	for key, want := range map[string]string{
		"short":            "s…rt",
		"sk-baidu":         "sk-…idu",
		"sk-wenxin":        "sk-…xin",
		"sk-a-1":           "sk…-1",
		"abc":              "…c",
		"ab":               "…",
		"0123456789abcdef": "0123…cdef",
	} {
		if got := multiKeyPreview(key); got != want {
			t.Fatalf("preview(%q) = %q, want %q", key, got, want)
		}
	}
}

// Whatever the length, the label must name the row without reproducing it: it
// never equals the key, always keeps the ellipsis, and stays valid UTF-8 even
// when the key is not ASCII.
func TestMultiKeyPreviewNeverRevealsTheWholeKey(t *testing.T) {
	keys := []string{"密钥一二三四五六七八九十"}
	for n := 1; n <= 40; n++ {
		keys = append(keys, strings.Repeat("k", n))
	}
	for _, key := range keys {
		got := multiKeyPreview(key)
		if got == "" || got == key || !strings.Contains(got, "…") {
			t.Fatalf("preview(%q) = %q", key, got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("preview(%q) split a multi-byte character: %q", key, got)
		}
		if strings.HasPrefix(got, "…") && len(key) > 3 {
			t.Fatalf("preview(%q) hides its head entirely: %q", key, got)
		}
	}
}

// The operator picks a row by the tail it ends with; two keys that differ only
// there must not share a label.
func TestMultiKeyPreviewSeparatesKeysThatDifferInTheTail(t *testing.T) {
	if multiKeyPreview("sk-1task-a") == multiKeyPreview("sk-1task-b") {
		t.Fatalf("tail is not part of the short-key preview")
	}
}

// The panel's three-key channel is the shape that broke: every key shorter
// than the 10+6 window came back as an empty key_preview, so all three rows
// were labelled only `#1 #2 #3`.
func TestMultiKeyStatusReturnsPreviewForShortKeys(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	channel := &model.Channel{
		Type:   constant.ChannelTypeOpenAI,
		Key:    "sk-baidu\nsk-wenxin\nsk-1task",
		Status: common.ChannelStatusEnabled,
		Name:   "short-keys",
		Models: "m",
		Group:  "default",
	}
	channel.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: 3, MultiKeyMode: constant.MultiKeyModePolling}
	require.NoError(t, channel.Insert())

	response, raw := callChannelAPI(t, ManageMultiKeys, http.MethodPost, "/api/channel/multi_key/manage",
		fmt.Sprintf(`{"channel_id":%d,"action":"get_key_status","page":1,"page_size":10}`, channel.Id))

	require.True(t, response.Success, raw)
	assert.Contains(t, raw, `"key_preview":"sk-…idu"`)
	assert.Contains(t, raw, `"key_preview":"sk-…xin"`)
	assert.Contains(t, raw, `"key_preview":"sk-…ask"`)
}

func TestMultiKeyPreviewTrimsWhitespace(t *testing.T) {
	got := multiKeyPreview("  eyJhbGciOiJIUzI1NiJ9.payload.tfLQLNs  ")
	if got != "eyJhbGciOi…fLQLNs" {
		t.Fatalf("preview = %q", got)
	}
}
