package blobstore

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validConfig() Config {
	return Config{
		Endpoint:   "http://127.0.0.1:9000",
		Bucket:     "apiduo-artifacts",
		Region:     "us-east-1",
		AccessKey:  "minioadmin",
		SecretKey:  "minioadmin",
		Prefix:     "gateway/v1",
		PathStyle:  true,
		PresignTTL: 15 * time.Minute,
	}
}

func TestConfigNormalizeAcceptsMinIOShape(t *testing.T) {
	config, err := validConfig().Normalize()
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9000", config.Endpoint)
	assert.True(t, config.PathStyle)
	assert.Equal(t, 15*time.Minute, config.PresignTTL)
}

func TestConfigNormalizeDefaults(t *testing.T) {
	config := validConfig()
	config.Region = ""
	config.Prefix = "/gateway/v1/"
	config.PresignTTL = 0

	normalized, err := config.Normalize()
	require.NoError(t, err)
	assert.Equal(t, DefaultRegion, normalized.Region)
	assert.Equal(t, "gateway/v1", normalized.Prefix)
	assert.Equal(t, DefaultPresignTTL, normalized.PresignTTL)
}

func TestConfigNormalizeRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		match  string
	}{
		{name: "empty endpoint", mutate: func(c *Config) { c.Endpoint = "" }, match: "endpoint is required"},
		{name: "relative endpoint", mutate: func(c *Config) { c.Endpoint = "minio:9000" }, match: "absolute URL"},
		{name: "userinfo", mutate: func(c *Config) { c.Endpoint = "http://user:pass@host:9000" }, match: "without userinfo"},
		{name: "query", mutate: func(c *Config) { c.Endpoint = "http://host:9000?x=1" }, match: "query or fragment"},
		{name: "scheme", mutate: func(c *Config) { c.Endpoint = "ftp://host:9000" }, match: "http or https"},
		{name: "empty bucket", mutate: func(c *Config) { c.Bucket = "" }, match: "bucket is required"},
		{name: "uppercase bucket", mutate: func(c *Config) { c.Bucket = "Apiduo" }, match: "bucket syntax"},
		{name: "ip bucket", mutate: func(c *Config) { c.Bucket = "192.168.1.1" }, match: "bucket syntax"},
		{name: "region", mutate: func(c *Config) { c.Region = "bad region" }, match: "region syntax"},
		{name: "access key whitespace", mutate: func(c *Config) { c.AccessKey = " key" }, match: "access key"},
		{name: "secret key control", mutate: func(c *Config) { c.SecretKey = "sec\nret" }, match: "secret key"},
		{name: "prefix traversal", mutate: func(c *Config) { c.Prefix = "a/../b" }, match: "dot segments"},
		{name: "prefix empty segment", mutate: func(c *Config) { c.Prefix = "a//b" }, match: "empty or dot segments"},
		{name: "ttl too long", mutate: func(c *Config) { c.PresignTTL = MaxPresignTTL + time.Hour }, match: "presign TTL"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			config := validConfig()
			testCase.mutate(&config)
			_, err := config.Normalize()
			require.Error(t, err)
			assert.ErrorContains(t, err, testCase.match)
		})
	}
}

func TestConfigWithPrefix(t *testing.T) {
	config := validConfig()
	assert.Equal(t, "gateway/v1/tasks/t1/video", config.WithPrefix("tasks/t1/video"))
	assert.Equal(t, "gateway/v1/tasks/t1/video", config.WithPrefix("/tasks/t1/video"))

	config.Prefix = ""
	assert.Equal(t, "tasks/t1/video", config.WithPrefix("tasks/t1/video"))
}

func TestValidateKey(t *testing.T) {
	require.NoError(t, ValidateKey("tasks/task-1/video"))
	require.NoError(t, ValidateKey("files/2026/11/06/a.png"))

	tests := []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "absolute", key: "/etc/passwd"},
		{name: "traversal", key: "tasks/../../etc/passwd"},
		{name: "dot segment", key: "tasks/./video"},
		{name: "empty segment", key: "tasks//video"},
		{name: "backslash", key: `tasks\video`},
		{name: "control", key: "tasks/\x00video"},
		{name: "too long", key: strings.Repeat("a", MaxKeyLength+1)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Error(t, ValidateKey(testCase.key))
		})
	}
}

func TestParseContentRangeTotal(t *testing.T) {
	assert.Equal(t, int64(4096), parseContentRangeTotal("bytes 0-1023/4096"))
	assert.Equal(t, int64(4096), parseContentRangeTotal("bytes 1024-2047/4096"))
	assert.Equal(t, int64(-1), parseContentRangeTotal("bytes 0-1023/*"))
	assert.Equal(t, int64(-1), parseContentRangeTotal(""))
	assert.Equal(t, int64(-1), parseContentRangeTotal("garbage"))
}
