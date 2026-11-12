package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Playground 的 /pg 前缀只是另一种凭据，请求本身与 /v1 同形。中间件负责把
// 路径就地归一化，让下游所有判定点（模式、渠道路径过滤、路由表）只看 /v1。
func TestPlaygroundPathAliasRewritesPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name      string
		method    string
		in        string
		wantPath  string
		wantQuery string
	}{
		{"chat", http.MethodPost, "/pg/chat/completions", "/v1/chat/completions", ""},
		{"images edits keeps query", http.MethodPost, "/pg/images/edits?group=default", "/v1/images/edits", "group=default"},
		{"video submit", http.MethodPost, "/pg/video/generations", "/v1/video/generations", ""},
		{"video fetch", http.MethodGet, "/pg/video/generations/abc123", "/v1/video/generations/abc123", ""},
		// 对外入口原样通过，不能被顺手牵羊。
		{"v1 untouched", http.MethodPost, "/v1/chat/completions", "/v1/chat/completions", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(tc.method, tc.in, nil)
			original := c.Request.URL.Path

			PlaygroundPathAlias()(c)

			assert.Equal(t, tc.wantPath, c.Request.URL.Path)
			assert.Equal(t, tc.wantQuery, c.Request.URL.RawQuery)
			assert.Empty(t, c.Request.URL.RawPath)
			assert.Equal(t, c.Request.URL.RequestURI(), c.Request.RequestURI)

			got := playgroundClientPath(c)
			if strings.HasPrefix(original, "/pg") {
				// playground 请求：真实路径必须留住，供日志回显。
				assert.Equal(t, original, got)
			} else {
				assert.Empty(t, got, "对外入口不该被记成 playground")
			}
		})
	}
}

// chat 的 playground 专属逻辑（分组校验）在路径改写之后仍要能认出请求来源。
func TestIsPlaygroundChatRequestAfterRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func(t *testing.T, path string) *gin.Context {
		t.Helper()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, nil)
		PlaygroundPathAlias()(c)
		return c
	}

	t.Run("playground chat", func(t *testing.T) {
		c := newContext(t, "/pg/chat/completions")
		assert.Equal(t, "/v1/chat/completions", c.Request.URL.Path)
		assert.True(t, isPlaygroundChatRequest(c))
	})

	t.Run("api chat", func(t *testing.T) {
		c := newContext(t, "/v1/chat/completions")
		assert.False(t, isPlaygroundChatRequest(c))
	})

	t.Run("playground image", func(t *testing.T) {
		c := newContext(t, "/pg/images/edits")
		assert.False(t, isPlaygroundChatRequest(c), "只有 chat 入口该命中分组校验")
	})
}

// IsPlayground 决定计费的独立分支，判定必须与路径改写同时成立。
func TestPlaygroundClientPathSurvivesRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/pg/images/generations", nil)

	require.Nil(t, c.Request.Context().Value(constant.ApiduoClientPathKey{}))
	PlaygroundPathAlias()(c)
	require.Equal(t, "/pg/images/generations", c.Request.Context().Value(constant.ApiduoClientPathKey{}))
}
