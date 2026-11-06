package model

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClickHouseLogInserterRoundTrip verifies the column order and value types
// of the native batch against a real ClickHouse instance. The GORM based unit
// tests cannot catch a misaligned column list, which would silently shift every
// field one column to the right.
//
// Enable with TEST_CLICKHOUSE_DSN pointing at a scratch database; the test
// drops and recreates the logs table there.
func TestClickHouseLogInserterRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		t.Skip("TEST_CLICKHOUSE_DSN is not configured")
	}

	inserter, err := newClickHouseLogInserter(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, inserter.Close()) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, inserter.conn.Exec(ctx, "DROP TABLE IF EXISTS logs"))
	require.NoError(t, inserter.conn.Exec(ctx, clickHouseLogCreateTableSQL(0)))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_ = inserter.conn.Exec(cleanupCtx, "DROP TABLE IF EXISTS logs")
	})

	now := time.Now().Unix()
	rows := []*Log{
		{
			UserId: 11, CreatedAt: now, Type: LogTypeConsume, Content: "consume", Username: "alice",
			TokenName: "tk-a", ModelName: "gpt-x", Quota: 1234, PromptTokens: 10, CompletionTokens: 20,
			UseTime: 3, IsStream: true, ChannelId: 7, TokenId: 9, Group: "vip", Ip: "1.2.3.4",
			RequestId: "req-1", UpstreamRequestId: "up-1", Other: `{"cache_tokens":5}`,
		},
		{
			UserId: 12, CreatedAt: now + 1, Type: LogTypeError, Content: "error", Username: "bob",
			ModelName: "gpt-y", IsStream: false, ChannelId: 8, RequestId: "req-2",
		},
	}
	require.NoError(t, inserter.Insert(ctx, rows))

	var total uint64
	require.NoError(t, inserter.conn.QueryRow(ctx, "SELECT count() FROM logs").Scan(&total))
	require.EqualValues(t, 2, total)

	var (
		userID     int32
		createdAt  int64
		logType    int32
		content    string
		username   string
		modelName  string
		quota      int32
		promptTok  int32
		compTok    int32
		useTime    int32
		isStream   uint8
		channelID  int32
		tokenID    int32
		groupName  string
		ip         string
		requestID  string
		upstreamID string
		other      string
	)
	require.NoError(t, inserter.conn.QueryRow(ctx, `
		SELECT user_id, created_at, type, content, username, model_name, quota,
		       prompt_tokens, completion_tokens, use_time, is_stream, channel_id, token_id,
		       `+"`group`"+`, ip, request_id, upstream_request_id, other
		FROM logs WHERE request_id = 'req-1'`).Scan(
		&userID, &createdAt, &logType, &content, &username, &modelName, &quota,
		&promptTok, &compTok, &useTime, &isStream, &channelID, &tokenID,
		&groupName, &ip, &requestID, &upstreamID, &other,
	))

	assert.EqualValues(t, 11, userID)
	assert.Equal(t, now, createdAt)
	assert.EqualValues(t, LogTypeConsume, logType)
	assert.Equal(t, "consume", content)
	assert.Equal(t, "alice", username)
	assert.Equal(t, "gpt-x", modelName)
	assert.EqualValues(t, 1234, quota)
	assert.EqualValues(t, 10, promptTok)
	assert.EqualValues(t, 20, compTok)
	assert.EqualValues(t, 3, useTime)
	assert.EqualValues(t, 1, isStream)
	assert.EqualValues(t, 7, channelID)
	assert.EqualValues(t, 9, tokenID)
	assert.Equal(t, "vip", groupName)
	assert.Equal(t, "1.2.3.4", ip)
	assert.Equal(t, "req-1", requestID)
	assert.Equal(t, "up-1", upstreamID)
	assert.Equal(t, `{"cache_tokens":5}`, other)
}

// BenchmarkClickHouseLogInserterBatch measures one 580 row native batch, which
// is the size a 5 second window produces at 116 rows per second.
func BenchmarkClickHouseLogInserterBatch(b *testing.B) {
	dsn := os.Getenv("TEST_CLICKHOUSE_DSN")
	if dsn == "" {
		b.Skip("TEST_CLICKHOUSE_DSN is not configured")
	}
	inserter, err := newClickHouseLogInserter(dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer inserter.Close()

	ctx := context.Background()
	if err := inserter.conn.Exec(ctx, "DROP TABLE IF EXISTS logs"); err != nil {
		b.Fatal(err)
	}
	if err := inserter.conn.Exec(ctx, clickHouseLogCreateTableSQL(0)); err != nil {
		b.Fatal(err)
	}
	defer func() { _ = inserter.conn.Exec(context.Background(), "DROP TABLE IF EXISTS logs") }()

	now := time.Now().Unix()
	rows := make([]*Log, 580)
	for i := range rows {
		rows[i] = &Log{
			UserId: i + 1, CreatedAt: now, Type: LogTypeConsume,
			ModelName: "gpt-bench", Quota: 10, Other: `{"cache_tokens":0,"model_ratio":1}`,
			RequestId: "bench-" + time.Now().Format("150405.000000000") + "-" + string(rune('a'+i%26)),
		}
	}

	b.ResetTimer()
	for range b.N {
		if err := inserter.Insert(ctx, rows); err != nil {
			b.Fatal(err)
		}
	}
}
