package router

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaykitdto "github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFileRelayEndToEnd drives the whole path against a stub provider: purpose
// selects the channel, the upload streams through, the gateway mints its own
// id, and content is replayed from the channel that stored it.
func TestFileRelayEndToEnd(t *testing.T) {
	setupRelayRouterTestDB(t)
	require.NoError(t, model.DB.AutoMigrate(&model.FileObject{}, &model.Channel{}))

	var uploads, downloads, deletes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/files":
			uploads.Add(1)
			reader, err := r.MultipartReader()
			require.NoError(t, err)
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				require.NoError(t, err)
				_, _ = io.Copy(io.Discard, part)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"file-up-1","object":"file","bytes":11,"created_at":1700000000,"filename":"note.txt","purpose":"file-extract","status":"processed"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/files/file-up-1/content":
			downloads.Add(1)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello world"))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/files/file-up-1":
			deletes.Add(1)
			_, _ = w.Write([]byte(`{"id":"file-up-1","deleted":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	user := model.User{Username: "file-e2e", Status: common.UserStatusEnabled, Group: "default", Quota: 100}
	require.NoError(t, model.DB.Create(&user).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId:         user.Id,
		Key:            "filee2ekey",
		Status:         common.TokenStatusEnabled,
		ExpiredTime:    -1,
		UnlimitedQuota: true,
	}).Error)

	channel := &model.Channel{
		Type:   constant.ChannelTypeOpenAI,
		Key:    "sk-upstream",
		Status: common.ChannelStatusEnabled,
		Group:  "default",
		Models: "gpt-4o",
		BaseURL: func() *string {
			value := upstream.URL
			return &value
		}(),
	}
	channel.SetOtherSettings(relaykitdto.ChannelOtherSettings{
		FileRelay: &relaykitdto.ChannelFileRelayConfig{Purposes: []string{"file-extract"}},
	})
	require.NoError(t, model.DB.Create(channel).Error)

	engine := gin.New()
	SetRelayRouter(engine)

	call := func(method, path string, body io.Reader, contentType string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, body)
		request.Header.Set("Authorization", "Bearer filee2ekey")
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		return recorder
	}

	upload := func(purpose string) *httptest.ResponseRecorder {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		part, err := writer.CreateFormFile("file", "note.txt")
		require.NoError(t, err)
		_, err = part.Write([]byte("hello world"))
		require.NoError(t, err)
		require.NoError(t, writer.WriteField("purpose", purpose))
		require.NoError(t, writer.Close())
		return call(http.MethodPost, "/v1/files", &buffer, writer.FormDataContentType())
	}

	created := upload("file-extract")
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	require.Equal(t, int32(1), uploads.Load())

	var object struct {
		Id       string `json:"id"`
		Object   string `json:"object"`
		Purpose  string `json:"purpose"`
		Filename string `json:"filename"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &object))
	assert.True(t, strings.HasPrefix(object.Id, "file-apiduo-"), "gateway must mint its own id, got %q", object.Id)
	assert.Equal(t, "file", object.Object)
	assert.Equal(t, "file-extract", object.Purpose)

	t.Run("list shows the gateway id", func(t *testing.T) {
		recorder := call(http.MethodGet, "/v1/files?purpose=file-extract", nil, "")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), object.Id)
		assert.NotContains(t, recorder.Body.String(), "file-up-1")
	})

	t.Run("retrieve returns metadata", func(t *testing.T) {
		recorder := call(http.MethodGet, "/v1/files/"+object.Id, nil, "")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), object.Id)
	})

	t.Run("content streams from the owning channel", func(t *testing.T) {
		recorder := call(http.MethodGet, "/v1/files/"+object.Id+"/content", nil, "")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, "hello world", recorder.Body.String())
		assert.Equal(t, int32(1), downloads.Load())
	})

	t.Run("unconfigured purpose is a routing gap, not a client error", func(t *testing.T) {
		recorder := upload("batch")
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		assert.Contains(t, recorder.Body.String(), "no_file_channel")
	})

	t.Run("another account cannot read the file", func(t *testing.T) {
		other := model.User{Username: "file-e2e-other", Status: common.UserStatusEnabled, Group: "default", Quota: 100, AffCode: "othere2e"}
		require.NoError(t, model.DB.Create(&other).Error)
		require.NoError(t, model.DB.Create(&model.Token{
			UserId: other.Id, Key: "otherkey", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
		}).Error)
		request := httptest.NewRequest(http.MethodGet, "/v1/files/"+object.Id+"/content", nil)
		request.Header.Set("Authorization", "Bearer otherkey")
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		assert.Equal(t, http.StatusNotFound, recorder.Code, recorder.Body.String())
	})

	t.Run("delete removes the record and the upstream object", func(t *testing.T) {
		recorder := call(http.MethodDelete, "/v1/files/"+object.Id, nil, "")
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, int32(1), deletes.Load())
		missing := call(http.MethodGet, "/v1/files/"+object.Id, nil, "")
		assert.Equal(t, http.StatusNotFound, missing.Code)
	})
}
