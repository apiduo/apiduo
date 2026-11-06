package router

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaykitdto "github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFileRelayRejectsAbusiveUploads covers the ways a caller can try to spend
// the gateway's resources without spending its own: unbounded bodies, empty
// files that cost nothing per megabyte, and rows whose expiry never takes
// effect.
func TestFileRelayRejectsAbusiveUploads(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.FileObject{}, &model.Channel{}))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-up-x","object":"file","bytes":6,"created_at":1700000000,"filename":"n.txt","purpose":"file-extract","status":"processed"}`))
	}))
	defer upstream.Close()

	user := model.User{Username: "abuse-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId: user.Id, Key: "abuseKey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
	}).Error)

	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Key: "sk-up", Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-4o",
	}
	base := upstream.URL
	channel.BaseURL = &base
	channel.SetOtherSettings(relaykitdto.ChannelOtherSettings{
		FileRelay: &relaykitdto.ChannelFileRelayConfig{Purposes: []string{"file-extract"}},
	})
	require.NoError(t, model.DB.Create(channel).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	call := func(request *http.Request) *httptest.ResponseRecorder {
		request.Header.Set("Authorization", "Bearer abuseKey")
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	multipartBody := func(filename string, payload []byte) (io.Reader, string) {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		part, err := writer.CreateFormFile("file", filename)
		require.NoError(t, err)
		if len(payload) > 0 {
			_, err = part.Write(payload)
			require.NoError(t, err)
		}
		require.NoError(t, writer.WriteField("purpose", "file-extract"))
		require.NoError(t, writer.Close())
		return &buffer, writer.FormDataContentType()
	}

	t.Run("empty file is rejected", func(t *testing.T) {
		body, contentType := multipartBody("empty.txt", nil)
		request := httptest.NewRequest(http.MethodPost, "/v1/files", body)
		request.Header.Set("Content-Type", contentType)
		recorder := call(request)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "empty_file")
	})

	t.Run("oversized body is rejected before it is parsed", func(t *testing.T) {
		body, contentType := multipartBody("big.bin", []byte("data"))
		request := httptest.NewRequest(http.MethodPost, "/v1/files", body)
		request.Header.Set("Content-Type", contentType)
		// Claim a gigabyte. The handler rejects on Content-Length, so nothing is
		// ever written to a spool file.
		request.ContentLength = int64(1) << 40
		recorder := call(request)
		require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "request_too_large")
	})

	t.Run("expired file disappears and stops resolving", func(t *testing.T) {
		now := time.Now().Unix()
		expired := &model.FileObject{
			GatewayId:   "file-apiduo-expired",
			UserId:      user.Id,
			ChannelId:   channel.Id,
			UpstreamId:  "file-up-x",
			Purpose:     "file-extract",
			Filename:    "old.txt",
			Bytes:       10,
			ContentKind: model.FileContentKindBytes,
			CreatedAt:   now - 3600,
			ExpiresAt:   now - 60,
		}
		require.NoError(t, model.CreateFileObject(expired))

		listing := call(httptest.NewRequest(http.MethodGet, "/v1/files", nil))
		require.Equal(t, http.StatusOK, listing.Code, listing.Body.String())
		assert.NotContains(t, listing.Body.String(), "file-apiduo-expired")

		detail := call(httptest.NewRequest(http.MethodGet, "/v1/files/file-apiduo-expired", nil))
		assert.Equal(t, http.StatusNotFound, detail.Code, detail.Body.String())

		content := call(httptest.NewRequest(http.MethodGet, "/v1/files/file-apiduo-expired/content", nil))
		assert.Equal(t, http.StatusNotFound, content.Code, content.Body.String())
	})

	t.Run("an upload sweeps the caller's expired rows", func(t *testing.T) {
		var before int64
		require.NoError(t, model.DB.Model(&model.FileObject{}).
			Where("gateway_id = ?", "file-apiduo-expired").Count(&before).Error)
		require.EqualValues(t, 1, before)

		body, contentType := multipartBody("fresh.txt", []byte("hello!"))
		request := httptest.NewRequest(http.MethodPost, "/v1/files", body)
		request.Header.Set("Content-Type", contentType)
		recorder := call(request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

		var after int64
		require.NoError(t, model.DB.Model(&model.FileObject{}).
			Where("gateway_id = ?", "file-apiduo-expired").Count(&after).Error)
		assert.EqualValues(t, 0, after, "the expired row still occupies the storage ceiling")
	})
}

// TestFileRelayContentTypeFollowsTheProvider guards the case the upload MIME
// gets wrong: a PDF whose content endpoint answers with extracted text must not
// be labelled application/pdf, or the client saves text as a broken PDF.
func TestFileRelayContentTypeFollowsTheProvider(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.FileObject{}, &model.Channel{}))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"file-up-pdf","object":"file","bytes":9,"created_at":1700000000,"filename":"r.pdf","purpose":"file-extract","status":"processed"}`))
		case strings.HasSuffix(r.URL.Path, "/content"):
			// Exactly what Moonshot answers: JSON envelope with text inside.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"content":"extracted text","file_type":"text/plain","filename":"r.pdf","type":"file"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	user := model.User{Username: "content-type-user", Status: common.UserStatusEnabled, Group: "default", Quota: 100000}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId: user.Id, Key: "ctKey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
	}).Error)

	// Type 1 on purpose: the gateway cannot know this upstream is Moonshot, so
	// the upload is classified as raw bytes and the old code fell back to the
	// upload MIME here.
	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Key: "sk-up", Status: common.ChannelStatusEnabled,
		Group: "default", Models: "gpt-4o",
	}
	base := upstream.URL
	channel.BaseURL = &base
	channel.SetOtherSettings(relaykitdto.ChannelOtherSettings{
		FileRelay: &relaykitdto.ChannelFileRelayConfig{Purposes: []string{"file-extract"}},
	})
	require.NoError(t, model.DB.Create(channel).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", "r.pdf")
	require.NoError(t, err)
	_, err = part.Write([]byte("%PDF-1.4 fakepdf"))
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("purpose", "file-extract"))
	require.NoError(t, writer.Close())

	upload := httptest.NewRequest(http.MethodPost, "/v1/files", &buffer)
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	upload.Header.Set("Authorization", "Bearer ctKey")
	uploadRecorder := httptest.NewRecorder()
	engine.ServeHTTP(uploadRecorder, upload)
	require.Equal(t, http.StatusOK, uploadRecorder.Code, uploadRecorder.Body.String())

	var created struct {
		Id string `json:"id"`
	}
	require.NoError(t, common.Unmarshal(uploadRecorder.Body.Bytes(), &created))
	require.NotEmpty(t, created.Id)

	content := httptest.NewRequest(http.MethodGet, "/v1/files/"+created.Id+"/content", nil)
	content.Header.Set("Authorization", "Bearer ctKey")
	contentRecorder := httptest.NewRecorder()
	engine.ServeHTTP(contentRecorder, content)

	require.Equal(t, http.StatusOK, contentRecorder.Code, contentRecorder.Body.String())
	assert.Equal(t, "extracted text", contentRecorder.Body.String())
	assert.Contains(t, contentRecorder.Header().Get("Content-Type"), "text/markdown")
	assert.NotContains(t, contentRecorder.Header().Get("Content-Type"), "application/pdf")
}
