package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/console_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupAnnouncementAPI 用内存库跑真实的落库路径。
//
// 这里不 mock model.UpdateOption：公告 API 的价值有一半在于
// 「写完立刻能在 GetConsoleSetting 读到」，换掉那一层就测不到了。
func setupAnnouncementAPI(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db

	previousValue := console_setting.GetConsoleSetting().Announcements
	console_setting.GetConsoleSetting().Announcements = ""
	// UpdateOption 会写 common.OptionMap，测试环境里它是 nil
	previousOptionMap := common.OptionMap
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		model.DB = previousDB
		console_setting.GetConsoleSetting().Announcements = previousValue
		common.OptionMap = previousOptionMap
	})

	engine := gin.New()
	engine.GET("/api/announcements", ListAnnouncements)
	engine.POST("/api/announcements", CreateAnnouncement)
	engine.PATCH("/api/announcements/:id", UpdateAnnouncement)
	engine.DELETE("/api/announcements/:id", DeleteAnnouncement)
	return engine
}

func announcementRequest(engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

// decodeAnnouncementList 读出接口返回的列表。
func decodeAnnouncementList(t *testing.T, response *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var payload struct {
		Success bool             `json:"success"`
		Data    []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.True(t, payload.Success, "response: %s", response.Body.String())
	return payload.Data
}

func TestCreateAnnouncementAssignsServerSideID(t *testing.T) {
	engine := setupAnnouncementAPI(t)

	first := announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"content":"第一条","publishDate":"2026-11-16T10:00:00+08:00"}`)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	// 请求体里带 id 也必须被服务端覆盖，否则调用方能撞 id
	second := announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"id":999,"content":"第二条","publishDate":"2026-11-17T10:00:00+08:00"}`)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())

	list := decodeAnnouncementList(t, announcementRequest(engine, http.MethodGet, "/api/announcements", ""))
	require.Len(t, list, 2)
	assert.Equal(t, 1, announcementID(list[0]))
	assert.Equal(t, 2, announcementID(list[1]), "id 999 must be ignored")
}

func TestUpdateAnnouncementPatchesOnlyGivenFields(t *testing.T) {
	engine := setupAnnouncementAPI(t)
	announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"content":"原文","title":"原标题","publishDate":"2026-11-16T10:00:00+08:00","extra":"保留我"}`)

	patch := announcementRequest(engine, http.MethodPatch, "/api/announcements/1", `{"title":"新标题"}`)
	require.Equal(t, http.StatusOK, patch.Code, patch.Body.String())

	list := decodeAnnouncementList(t, announcementRequest(engine, http.MethodGet, "/api/announcements", ""))
	require.Len(t, list, 1)
	assert.Equal(t, "新标题", list[0]["title"])
	assert.Equal(t, "原文", list[0]["content"], "未提供的字段必须保留")
	assert.Equal(t, "保留我", list[0]["extra"])
}

func TestUpdateAnnouncementDropsEmptyOptionalFields(t *testing.T) {
	engine := setupAnnouncementAPI(t)
	announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"content":"正文","title":"标题","publishDate":"2026-11-16T10:00:00+08:00","expiresAt":"2026-11-20T00:00:00+08:00"}`)

	// 前端清空标题时发的是空串
	announcementRequest(engine, http.MethodPatch, "/api/announcements/1", `{"title":"  ","expiresAt":""}`)

	list := decodeAnnouncementList(t, announcementRequest(engine, http.MethodGet, "/api/announcements", ""))
	require.Len(t, list, 1)
	assert.NotContains(t, list[0], "title", "空标题必须被删掉，否则列表的回落逻辑失效")
	assert.NotContains(t, list[0], "expiresAt", "空失效时间必须被删掉")
}

func TestDeleteAnnouncement(t *testing.T) {
	engine := setupAnnouncementAPI(t)
	announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"content":"保留","publishDate":"2026-11-16T10:00:00+08:00"}`)
	announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"content":"删除","publishDate":"2026-11-17T10:00:00+08:00"}`)

	response := announcementRequest(engine, http.MethodDelete, "/api/announcements/2", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	list := decodeAnnouncementList(t, announcementRequest(engine, http.MethodGet, "/api/announcements", ""))
	require.Len(t, list, 1)
	assert.Equal(t, "保留", list[0]["content"])
}

func TestAnnouncementMissingIDReturns404(t *testing.T) {
	engine := setupAnnouncementAPI(t)
	announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"content":"唯一一条","publishDate":"2026-11-16T10:00:00+08:00"}`)

	patch := announcementRequest(engine, http.MethodPatch, "/api/announcements/42", `{"title":"x"}`)
	assert.Equal(t, http.StatusNotFound, patch.Code)

	del := announcementRequest(engine, http.MethodDelete, "/api/announcements/42", "")
	assert.Equal(t, http.StatusNotFound, del.Code)
}

// 这条锁的是「新接口没有绕过校验」：expiresAt 早于 publishDate 必须被拒，
// 与 /api/option 那条路径的判断完全一致。
func TestCreateAnnouncementRejectsInvalidWindow(t *testing.T) {
	engine := setupAnnouncementAPI(t)
	response := announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"content":"x","publishDate":"2026-11-16T10:00:00+08:00","expiresAt":"2026-11-10T00:00:00+08:00"}`)
	// 项目的业务错误走 HTTP 200 + success:false，不是 4xx
	assert.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success, "invalid window must be rejected")
	assert.Contains(t, payload.Message, "expiry must be later")
}

// 管理视角必须看得到已过期的公告，否则过期的改不了也删不掉。
func TestListAnnouncementsIncludesExpired(t *testing.T) {
	engine := setupAnnouncementAPI(t)
	announcementRequest(engine, http.MethodPost, "/api/announcements",
		`{"content":"已结束的维护","publishDate":"2020-01-01T00:00:00+08:00","expiresAt":"2020-01-02T00:00:00+08:00"}`)

	list := decodeAnnouncementList(t, announcementRequest(engine, http.MethodGet, "/api/announcements", ""))
	assert.Len(t, list, 1, "管理接口不过滤时间窗口")

	// 而读者侧应当看不到它
	assert.Empty(t, console_setting.GetAnnouncements())
}
