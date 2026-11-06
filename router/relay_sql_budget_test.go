package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Budget for one /v1/chat/completions call. Every new query here is paid on
// every model request, so the number is a hard ceiling rather than a rough
// guide: raising it needs a reason in the diff, not just a green build.
//
// Measured baseline (SQLite, no Redis, no memory cache):
//
//	1  SELECT tokens WHERE key
//	2  SELECT users
//	3  SELECT abilities
//	4  SELECT channels WHERE id IN
//	5  SELECT channels WHERE id            <- same row, fetched twice upstream
//	6  SELECT count(*) user_subscriptions
//	7  SELECT quota FROM users
//	8  UPDATE users request_count/used_quota
//	9  UPDATE channels used_quota
//	10 UPDATE users quota
//	11 UPDATE tokens
//	12 SELECT setting FROM users
//	13 INSERT logs
//
// With MEMORY_CACHE_ENABLED the abilities and channels reads come from memory
// instead: 5 reads + 5 writes. That is the deployed configuration (Redis implies
// the memory cache), which is why both paths get their own budget.
//
// Five of the statements are writes. Writes are the part that scales with
// traffic and cannot be cached away, so they get their own budget: a new read is
// a warning, a new write is a capacity problem.
const (
	// Without channel cache: abilities and channels are read per request.
	relayReadBudgetNoCache = 8
	// With MEMORY_CACHE_ENABLED: abilities and channels come from memory.
	relayReadBudgetCached = 5
	relayWriteBudget      = 5
)

// sqlBudgetProbe counts statements executed between Reset and Snapshot.
type sqlBudgetProbe struct {
	mu     sync.Mutex
	reads  int
	writes int
	stmts  []string
}

func (p *sqlBudgetProbe) Name() string { return "test:sql-budget" }

func (p *sqlBudgetProbe) Initialize(db *gorm.DB) error {
	record := func(kind string, write bool) func(*gorm.DB) {
		return func(tx *gorm.DB) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if write {
				p.writes++
			} else {
				p.reads++
			}
			p.stmts = append(p.stmts, kind+": "+tx.Statement.SQL.String())
		}
	}

	if err := db.Callback().Query().After("gorm:query").Register("test:sql-budget:read", record("read", false)); err != nil {
		return err
	}
	if err := db.Callback().Create().After("gorm:create").Register("test:sql-budget:create", record("write", true)); err != nil {
		return err
	}
	if err := db.Callback().Update().After("gorm:update").Register("test:sql-budget:update", record("write", true)); err != nil {
		return err
	}
	if err := db.Callback().Delete().After("gorm:delete").Register("test:sql-budget:delete", record("write", true)); err != nil {
		return err
	}
	return nil
}

func (p *sqlBudgetProbe) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads = 0
	p.writes = 0
	p.stmts = nil
}

func (p *sqlBudgetProbe) Snapshot() (reads int, writes int, stmts []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reads, p.writes, append([]string(nil), p.stmts...)
}

// stubChatUpstream answers a minimal OpenAI-compatible completion so the relay
// path runs end to end without a provider.
func stubChatUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	body := `{"id":"chatcmpl-budget","object":"chat.completion","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func seedRelayBudgetFixture(t *testing.T, upstreamURL string) (user model.User, tokenKey string) {
	t.Helper()

	user = model.User{
		Username: "sql-budget-user",
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    1_000_000,
	}
	require.NoError(t, model.DB.Create(&user).Error)

	// Stored keys have no "sk-" prefix; clients send it.
	tokenKey = "sqlbudgettoken00000000000000000000000000000001"
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            tokenKey,
		Name:           "sql-budget",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	channel := model.Channel{
		Name:    "budget-stub",
		Type:    1,
		Key:     "stub-key",
		BaseURL: &upstreamURL,
		Models:  "gpt-4o",
		Group:   "default",
		Status:  common.ChannelStatusEnabled,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "default",
		Model:     "gpt-4o",
		ChannelId: channel.Id,
		Enabled:   true,
	}).Error)

	return user, tokenKey
}

func postChatCompletion(t *testing.T, engine *gin.Engine, tokenKey string) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"model":    "gpt-4o",
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	})
	require.NoError(t, err)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer sk-"+tokenKey)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}

// TestRelaySQLBudget pins how many statements one chat completion costs.
//
// Its purpose is not to measure speed but to make a silent regression loud:
// a query added to the hot path shows up here as a failing diff instead of as
// a production incident months later. If you intentionally add or remove a
// query, update the constants and the list above in the same commit.
func TestRelaySQLBudget(t *testing.T) {
	t.Run("without cache", func(t *testing.T) {
		runRelaySQLBudget(t, false, relayReadBudgetNoCache, relayWriteBudget)
	})
	// Production runs with MEMORY_CACHE_ENABLED (Redis implies it), so the
	// cached path needs its own budget: a cache regression must fail too.
	t.Run("with memory cache", func(t *testing.T) {
		runRelaySQLBudget(t, true, relayReadBudgetCached, relayWriteBudget)
	})
}

func runRelaySQLBudget(t *testing.T, memoryCache bool, wantReads, wantWrites int) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(
		&model.Channel{},
		&model.Log{},
		&model.Option{},
		&model.UserSubscription{},
	))

	// The relay refuses to bill an unconfigured model, and the error path needs
	// the i18n bundle. Pin a price so the test exercises the normal path.
	require.NoError(t, i18n.Init())
	ratio_setting.InitRatioSettings()
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o":1}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"gpt-4o":1}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))

	upstream := stubChatUpstream(t)
	_, tokenKey := seedRelayBudgetFixture(t, upstream.URL)

	if memoryCache {
		previous := common.MemoryCacheEnabled
		common.MemoryCacheEnabled = true
		t.Cleanup(func() { common.MemoryCacheEnabled = previous })
		model.InitChannelCache()
	}

	probe := &sqlBudgetProbe{}
	require.NoError(t, model.DB.Use(probe))

	engine := gin.New()
	SetRelayRouter(engine)

	// Warm up so first-call one-offs (config load, prepared statements) land
	// outside the measured window.
	for i := 0; i < 3; i++ {
		recorder := postChatCompletion(t, engine, tokenKey)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	}

	const samples = 5
	for i := 0; i < samples; i++ {
		probe.Reset()
		recorder := postChatCompletion(t, engine, tokenKey)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

		reads, writes, stmts := probe.Snapshot()
		if reads != wantReads || writes != wantWrites {
			t.Fatalf(
				"relay SQL budget changed: got %d reads + %d writes, want %d + %d\nstatements:\n%s",
				reads, writes, wantReads, wantWrites, formatStatements(stmts),
			)
		}
	}
}

func formatStatements(stmts []string) string {
	var out strings.Builder
	seen := map[string]struct{}{}
	for _, stmt := range stmts {
		key := stmt
		if len(key) > 160 {
			key = key[:160]
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out.WriteString("  - " + key + "\n")
	}
	fmt.Fprintf(&out, "  (%d statements, %d unique)", len(stmts), len(seen))
	return out.String()
}
