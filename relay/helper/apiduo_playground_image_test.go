package helper

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// playground 的 /pg 前缀在进入分发器之前就被 middle.PlaygroundPathAlias
// 归一化成 /v1（playground 只是另一种凭据），所以这里锁的是**改写之后**的形状：
// 一条标准的 multipart 图片编辑请求，n 必须按整数解析。
//
// 回归背景：`n` 在表单里恒为字符串，一旦走成「表单值原样转 JSON」的默认分支，
// 就会写进 *uint 报 `cannot unmarshal string into Go struct field Alias.n`。
// 而模式判定只认 /v1 前缀 —— 这也是把归一化放在入口、而不是散在判定点的原因。
func TestGetAndValidateRequestImageEditMultipart(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func(t *testing.T, nValue string) *gin.Context {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "doubao-seedream-5-0-flash-260915"))
		require.NoError(t, writer.WriteField("prompt", "keep the gradient"))
		require.NoError(t, writer.WriteField("size", "1024x1024"))
		require.NoError(t, writer.WriteField("n", nValue))
		part, err := writer.CreateFormFile("image", "ref.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("fake image"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return c
	}

	t.Run("multipart n is parsed as an integer", func(t *testing.T) {
		request, err := GetAndValidateRequest(newContext(t, "2"), types.RelayFormatOpenAIImage)
		require.NoError(t, err)

		imageRequest, ok := request.(*dto.ImageRequest)
		require.True(t, ok, "expected *dto.ImageRequest, got %T", request)
		require.NotNil(t, imageRequest.N)
		assert.Equal(t, uint(2), *imageRequest.N)
		assert.Equal(t, "doubao-seedream-5-0-flash-260915", imageRequest.Model)
		assert.Equal(t, "keep the gradient", imageRequest.Prompt)
		assert.Equal(t, "1024x1024", imageRequest.Size)
	})

	t.Run("non-numeric n is a client error", func(t *testing.T) {
		_, err := GetAndValidateRequest(newContext(t, "abc"), types.RelayFormatOpenAIImage)
		require.Error(t, err)
	})
}
