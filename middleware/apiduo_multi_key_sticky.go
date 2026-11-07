package middleware

import (
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

const apiduoMultiKeyTriedContextKey = "apiduo_multi_key_tried"

// apiduoMultiKeyTried remembers which key indexes a single request already
// used, so a retry moves to the next candidate instead of landing on the key
// that just failed. It is scoped per channel: indexes mean nothing across
// channels, and a retry may select a different one.
//
// The mutex is not decoration. An ordinary relay retries serially, but a
// websocket connection reuses one gin context across messages, so two frames can
// reach key selection concurrently.
type apiduoMultiKeyTried struct {
	channelID int
	mu        sync.Mutex
	indexes   map[int]struct{}
}

// pick runs under the lock so choosing a key and recording it are one step.
// Splitting them lets two concurrent frames pick the same index and then both
// record it, which is exactly the retry collision this set exists to prevent.
func (t *apiduoMultiKeyTried) pick(selectKey func(skip map[int]struct{}) (string, int, *types.NewAPIError)) (string, int, *types.NewAPIError) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key, index, apiErr := selectKey(t.indexes)
	if apiErr == nil && index >= 0 {
		t.indexes[index] = struct{}{}
	}
	return key, index, apiErr
}

// overrideStickyChannelKey replaces the key the regular rotation just picked
// when the channel uses the sticky strategy. Every other strategy returns the
// upstream result untouched, which is what keeps default behaviour identical.
//
// It runs after GetNextEnabledKey rather than instead of it, so the upstream call
// site keeps its own line: a rebase conflicts on changed lines, and leaving that
// line alone removes this feature from the conflict surface entirely. That call
// is not wasted work either — sticky channels fall into the random branch there
// (see GetNextEnabledKey), so the value it returns is a usable fallback for the
// case where sticky selection fails.
func overrideStickyChannelKey(c *gin.Context, channel *model.Channel, key string, index int, apiErr *types.NewAPIError) (string, int, *types.NewAPIError) {
	if channel == nil || channel.ChannelInfo.MultiKeyMode != constant.MultiKeyModeSticky {
		return key, index, apiErr
	}
	if apiErr != nil {
		return key, index, apiErr
	}

	tried := apiduoTried(c, channel.Id)
	seed := apiduoStickySeed(c)
	stickyKey, stickyIndex, stickyErr := tried.pick(func(skip map[int]struct{}) (string, int, *types.NewAPIError) {
		return channel.GetStickyEnabledKey(seed, skip)
	})
	if stickyErr != nil {
		// Sticky routing must never fail a request upstream would have served.
		return key, index, apiErr
	}
	return stickyKey, stickyIndex, nil
}

// apiduoStickySeed makes one downstream credential own one upstream key. A
// token is the finest unit the gateway can rely on: the same API key therefore
// keeps hitting the same upstream key, and every prompt cache entry that
// upstream keeps for that key stays warm.
//
// Token first, then user: a user-level fallback at least keeps a single
// account's traffic together when the token id is unavailable. An empty seed
// makes GetStickyEnabledKey fall back to rotation, so an unknown caller gets
// working routing rather than an error.
func apiduoStickySeed(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if tokenID := c.GetInt("token_id"); tokenID > 0 {
		return "token:" + strconv.Itoa(tokenID)
	}
	if userID := c.GetInt("id"); userID > 0 {
		return "user:" + strconv.Itoa(userID)
	}
	return ""
}

// apiduoTriedInitMu guards the get-or-create below. Without it two concurrent
// frames both find no entry, both create one, and each ends up locking its own
// mutex — so the per-request lock protects nothing and both frames can pick the
// same key. The critical section is a map lookup plus a small allocation.
var apiduoTriedInitMu sync.Mutex

func apiduoTried(c *gin.Context, channelID int) *apiduoMultiKeyTried {
	// Fast path: the entry exists for every attempt after the first, and that is
	// all requests in steady state. The lock below is only for the first one.
	if tried, ok := apiduoExistingTried(c, channelID); ok {
		return tried
	}

	apiduoTriedInitMu.Lock()
	defer apiduoTriedInitMu.Unlock()

	// Re-check under the lock: two frames can both miss the fast path and must
	// not each create their own instance, or their mutexes stop being shared.
	if tried, ok := apiduoExistingTried(c, channelID); ok {
		return tried
	}

	tried := &apiduoMultiKeyTried{channelID: channelID, indexes: make(map[int]struct{}, 4)}
	if c != nil {
		c.Set(apiduoMultiKeyTriedContextKey, tried)
	}
	return tried
}

func apiduoExistingTried(c *gin.Context, channelID int) (*apiduoMultiKeyTried, bool) {
	if c == nil {
		return nil, false
	}
	value, ok := c.Get(apiduoMultiKeyTriedContextKey)
	if !ok {
		return nil, false
	}
	tried, ok := value.(*apiduoMultiKeyTried)
	if !ok || tried.channelID != channelID {
		return nil, false
	}
	return tried, true
}
