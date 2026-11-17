package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/endpoint"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func apiduoLocalImageTestInfo(channelType, apiType int, base string, settings kitdto.ChannelOtherSettings) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:          channelType,
		ChannelBaseUrl:       base,
		ApiType:              apiType,
		ChannelOtherSettings: settings,
	}}
}

// 一个渠道的图片放在哪里，是渠道自己说的。worker 不能假设 /v1/images/generations：
// 那对 OpenAI 形状的渠道成立，对其余渠道每一条都是错的。
func TestApiduoLocalImageUpstreamURL(t *testing.T) {
	t.Run("主端点是图片路径时以它为准", func(t *testing.T) {
		info := apiduoLocalImageTestInfo(constant.ChannelTypeDoubaoVideo, constant.APITypeOpenAI,
			"https://ark.example.com",
			kitdto.ChannelOtherSettings{PrimaryEndpoint: "/api/v3/images/generations"})

		url, err := apiduoLocalImageUpstreamURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://ark.example.com/api/v3/images/generations", url)
	})

	t.Run("主端点不是图片路径时也以它为准", func(t *testing.T) {
		info := apiduoLocalImageTestInfo(constant.ChannelTypeOpenAI, constant.APITypeOpenAI,
			"https://vendor.example.com",
			kitdto.ChannelOtherSettings{PrimaryEndpoint: "/v3/generate"})

		url, err := apiduoLocalImageUpstreamURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://vendor.example.com/v3/generate", url,
			"运营声明的地址优先于网关的猜测；发错了由上游报错，比静默改道可诊断")
	})

	t.Run("没声明主端点时问适配器", func(t *testing.T) {
		info := apiduoLocalImageTestInfo(constant.ChannelTypeVolcEngine, constant.APITypeVolcEngine,
			"https://ark.example.com", kitdto.ChannelOtherSettings{})

		url, err := apiduoLocalImageUpstreamURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://ark.example.com/api/v3/images/generations", url,
			"火山把图片放 /api/v3，与 OpenAI 不同；这里必须走它自己的适配器")
	})

	t.Run("主端点尾斜杠归一", func(t *testing.T) {
		info := apiduoLocalImageTestInfo(constant.ChannelTypeDoubaoVideo, constant.APITypeOpenAI,
			"https://ark.example.com",
			kitdto.ChannelOtherSettings{PrimaryEndpoint: "/api/v3/images/generations/"})

		url, err := apiduoLocalImageUpstreamURL(info)
		require.NoError(t, err)
		assert.Equal(t, "https://ark.example.com/api/v3/images/generations", url)
	})

	t.Run("没有适配器的渠道直接报错", func(t *testing.T) {
		info := apiduoLocalImageTestInfo(constant.ChannelTypeTaskPlugin, -1,
			"https://plugin.example.com", kitdto.ChannelOtherSettings{})

		_, err := apiduoLocalImageUpstreamURL(info)
		require.Error(t, err, "构造不出地址就该在提交时拒绝，而不是落库后失败")
	})

	t.Run("没有 base url 的渠道直接报错", func(t *testing.T) {
		info := apiduoLocalImageTestInfo(constant.ChannelTypeDoubaoVideo, constant.APITypeOpenAI,
			"", kitdto.ChannelOtherSettings{PrimaryEndpoint: "/api/v3/images/generations"})

		_, err := apiduoLocalImageUpstreamURL(info)
		require.Error(t, err, "只有路径没有主机，worker 发不出去")
	})
}

// 渠道把别名映射成上游模型名后，冻结下来的请求体必须带上映射后的名字，
// 否则 worker 重放时又把它改回去了。
func TestApiduoLocalImageAsyncWithModel(t *testing.T) {
	raw := []byte(`{"model":"alias","prompt":"a red cube","seed":9007199254740993,"n":1}`)

	out, taskErr := apiduoLocalImageAsyncWithModel(raw, "vendor-model")
	require.Nil(t, taskErr)

	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(out))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&decoded))

	assert.Equal(t, "vendor-model", decoded["model"])
	assert.Equal(t, "a red cube", decoded["prompt"])
	seed, ok := decoded["seed"].(json.Number)
	require.True(t, ok)
	assert.Equal(t, "9007199254740993", seed.String(),
		"大整数不能被 float64 舍入：换一个种子就是换一张图")
	count, ok := decoded["n"].(json.Number)
	require.True(t, ok)
	assert.Equal(t, "1", count.String())
}

// 重试会换渠道：上下文里的 channel_type 是本次尝试的真相，ChannelMeta 还停在
// 上一次尝试上。读错就会把「8001 失败后回落到同步渠道」重新送回 8001 链路，
// 而那正是这条特性存在的理由。
func TestApiduoLocalImageAsyncHandlesUsesCurrentAttemptChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, endpoint.AsyncImageSubmitPath,
		strings.NewReader(`{"model":"m","prompt":"x"}`))
	c.Set("channel_type", constant.ChannelTypeOpenAI)

	// 上一轮是 8001：如果这里读的是 ChannelMeta，就会被判成「仍是 8001」。
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeAdvancedAsync,
	}}

	previous := apiduoLocalImageAsyncEnabled
	apiduoLocalImageAsyncEnabled = func() bool { return true }
	t.Cleanup(func() { apiduoLocalImageAsyncEnabled = previous })

	if !apiduoLocalImageAsyncHandles(c, info) {
		t.Fatal("本次尝试是同步渠道，应该走本地执行")
	}

	c.Set("channel_type", constant.ChannelTypeAdvancedAsync)
	if apiduoLocalImageAsyncHandles(c, info) {
		t.Fatal("本次尝试是 8001，必须走上游异步链路")
	}
}
