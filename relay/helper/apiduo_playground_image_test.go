package helper

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// playground 的图片编辑入口是 /pg/images/edits，不在 /v1 前缀下。
// 回归锁：Path2RelayMode 必须认出它，否则 GetAndValidOpenAIImageRequest
// 会落到「表单值原样转 JSON」的默认分支，n 作为字符串写进 *uint 直接失败，
// 线上表现是 400 `cannot unmarshal string into Go struct field Alias.n`。
func TestGetAndValidateRequestPlaygroundImageEditMultipart(t *testing.T) {
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
		c.Request = httptest.NewRequest(http.MethodPost, "/pg/images/edits", bytes.NewReader(body.Bytes()))
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

	t.Run("path maps to the edit relay mode", func(t *testing.T) {
		assert.Equal(t, relayconstant.RelayModeImagesEdits, relayconstant.Path2RelayMode("/pg/images/edits"))
	})
}
