package router

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaykitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFileUploadChargesQuotaAndEnforcesLimits covers the guard rails that stop
// the relay from becoming free storage: the charge is taken per started
// megabyte before the upload leaves the gateway, and both the rolling upload
// window and the held-bytes ceiling refuse before a provider is contacted.
func TestFileUploadChargesQuotaAndEnforcesLimits(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.FileObject{}, &model.Channel{}))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-up-q","object":"file","bytes":11,"created_at":1700000000,"filename":"n.txt","purpose":"file-extract"}`))
	}))
	defer upstream.Close()

	settings := system_setting.GetFileRelaySetting()
	original := *settings
	t.Cleanup(func() { *settings = original })
	*settings = system_setting.FileRelaySetting{Enabled: true, QuotaPerMB: 1000}

	user := model.User{Username: "quota-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100000}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{
		UserId:      user.Id,
		Key:         "quotakey",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: 10000,
	}
	require.NoError(t, model.DB.Create(&token).Error)

	channel := &model.Channel{
		Type:   constant.ChannelTypeOpenAI,
		Key:    "sk-up",
		Status: common.ChannelStatusEnabled,
		Group:  "default",
		Models: "gpt-4o",
	}
	base := upstream.URL
	channel.BaseURL = &base
	channel.SetOtherSettings(relaykitdto.ChannelOtherSettings{
		FileRelay: &relaykitdto.ChannelFileRelayConfig{Purposes: []string{"file-extract"}},
	})
	require.NoError(t, model.DB.Create(channel).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	upload := func() *httptest.ResponseRecorder {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		part, err := writer.CreateFormFile("file", "n.txt")
		require.NoError(t, err)
		_, err = part.Write([]byte("hello world"))
		require.NoError(t, err)
		require.NoError(t, writer.WriteField("purpose", "file-extract"))
		require.NoError(t, writer.Close())

		request := httptest.NewRequest(http.MethodPost, "/v1/files", &buffer)
		request.Header.Set("Authorization", "Bearer quotakey")
		request.Header.Set("Content-Type", writer.FormDataContentType())
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	first := upload()
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	var chargedToken model.Token
	require.NoError(t, model.DB.First(&chargedToken, token.Id).Error)
	assert.Equal(t, 9000, chargedToken.RemainQuota, "11 bytes must round up to one megabyte and charge 1000")

	t.Run("rolling window rejects the next upload", func(t *testing.T) {
		*settings = system_setting.FileRelaySetting{Enabled: true, QuotaPerMB: 1000, DailyUploadLimit: 1}
		recorder := upload()
		require.Equal(t, http.StatusTooManyRequests, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "daily_upload_limit")

		var untouched model.Token
		require.NoError(t, model.DB.First(&untouched, token.Id).Error)
		assert.Equal(t, 9000, untouched.RemainQuota, "a refused upload must not reserve quota")
	})

	t.Run("held bytes ceiling rejects before contacting the provider", func(t *testing.T) {
		*settings = system_setting.FileRelaySetting{Enabled: true, QuotaPerMB: 1000, UserStorageLimitBytes: 11}
		recorder := upload()
		require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "storage_quota_exceeded")
	})

	t.Run("master switch answers 503", func(t *testing.T) {
		*settings = system_setting.FileRelaySetting{Enabled: false}
		recorder := upload()
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "file_relay_disabled")
	})
}
