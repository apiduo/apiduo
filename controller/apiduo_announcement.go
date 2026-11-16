package controller

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/console_setting"

	"github.com/gin-gonic/gin"
)

// 公告的管理视角 CRUD（APIDUO:announcement）。
//
// 与 /api/status 的 console_setting.GetAnnouncements 分工不同：
//   - 那边面向读者，按 [publishDate, expiresAt) 过滤，只看得到当前有效的
//   - 这边面向管理员与 agent，**不过滤**——否则已过期的公告改不了也删不掉
//
// 为什么开新接口而不让 agent 直接 PUT /api/option：
// option 接口是整包覆盖，调用方必须先读全量、改数组、再写回，两个并发请求
// 会互相覆盖。这里按 id 单条写，天然没有这个问题。
const announcementsOptionKey = "console_setting.announcements"

// 公告是单个 JSON 数组，读改写必须串行。进程内互斥只覆盖单实例；
// 多实例部署下仍有窗口，但公告是低频人工操作，不值得为它引入分布式锁。
var announcementsWriteMu sync.Mutex

func readAnnouncements() ([]map[string]any, error) {
	raw := console_setting.GetConsoleSetting().Announcements
	if strings.TrimSpace(raw) == "" {
		return []map[string]any{}, nil
	}
	var list []map[string]any
	if err := common.UnmarshalJsonStr(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// persistAnnouncements 走与 /api/option 完全相同的校验与落库路径，
// 因此这轮为 phases / expiresAt / title 加的规则在这里自动生效。
func persistAnnouncements(list []map[string]any) error {
	encoded, err := common.Marshal(list)
	if err != nil {
		return err
	}
	value := string(encoded)
	if err := console_setting.ValidateConsoleSettings(value, "Announcements"); err != nil {
		return err
	}
	return model.UpdateOption(announcementsOptionKey, value)
}

// announcementID 读整数 id。JSON 解出来的数字是 float64，
// 但同一个值经过 Go 侧写入后可能是 int，两种都要认。
func announcementID(item map[string]any) int {
	switch v := item["id"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

func nextAnnouncementID(list []map[string]any) int {
	maxID := 0
	for _, item := range list {
		if id := announcementID(item); id > maxID {
			maxID = id
		}
	}
	return maxID + 1
}

// dropEmptyOptionalFields 清掉空的可选字段。
//
// 前端把「未填写」序列化成空串时，空 title 会让列表的回落逻辑失效
// （空串是真值，不会走到 getPreviewText），空 expiresAt 则会被当成
// 「有这个字段但解析不了」——虽然过滤逻辑容错，但存脏值没有意义。
func dropEmptyOptionalFields(item map[string]any) {
	for _, key := range []string{"title", "expiresAt", "extra"} {
		if s, ok := item[key].(string); ok && strings.TrimSpace(s) == "" {
			delete(item, key)
		}
	}
}

// ListAnnouncements 返回全部公告，含未到发布时间与已过期的。
func ListAnnouncements(c *gin.Context) {
	list, err := readAnnouncements()
	if err != nil {
		common.ApiErrorMsg(c, "Stored announcements are not valid JSON")
		return
	}
	common.ApiSuccess(c, list)
}

// CreateAnnouncement 新增一条。id 由服务端分配，请求体里的 id 会被忽略。
func CreateAnnouncement(c *gin.Context) {
	var payload map[string]any
	if err := common.DecodeJson(c.Request.Body, &payload); err != nil {
		common.ApiErrorMsg(c, "Invalid parameters")
		return
	}
	dropEmptyOptionalFields(payload)

	announcementsWriteMu.Lock()
	defer announcementsWriteMu.Unlock()

	list, err := readAnnouncements()
	if err != nil {
		common.ApiErrorMsg(c, "Stored announcements are not valid JSON")
		return
	}
	// 服务端分配 id：让调用方决定 id 会撞车，也没法保证单调
	payload["id"] = nextAnnouncementID(list)

	if err := persistAnnouncements(append(list, payload)); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, payload)
}

// UpdateAnnouncement 按 id 局部更新：只覆盖请求体里出现的字段。
func UpdateAnnouncement(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "Invalid announcement id")
		return
	}
	var patch map[string]any
	if err := common.DecodeJson(c.Request.Body, &patch); err != nil {
		common.ApiErrorMsg(c, "Invalid parameters")
		return
	}

	announcementsWriteMu.Lock()
	defer announcementsWriteMu.Unlock()

	list, err := readAnnouncements()
	if err != nil {
		common.ApiErrorMsg(c, "Stored announcements are not valid JSON")
		return
	}
	index := -1
	for i, item := range list {
		if announcementID(item) == id {
			index = i
			break
		}
	}
	if index < 0 {
		common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Announcement not found"))
		return
	}

	for key, value := range patch {
		if key == "id" {
			continue
		}
		list[index][key] = value
	}
	dropEmptyOptionalFields(list[index])

	if err := persistAnnouncements(list); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, list[index])
}

func DeleteAnnouncement(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "Invalid announcement id")
		return
	}

	announcementsWriteMu.Lock()
	defer announcementsWriteMu.Unlock()

	list, err := readAnnouncements()
	if err != nil {
		common.ApiErrorMsg(c, "Stored announcements are not valid JSON")
		return
	}
	remaining := slices.DeleteFunc(list, func(item map[string]any) bool {
		return announcementID(item) == id
	})
	if len(remaining) == len(list) {
		common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Announcement not found"))
		return
	}

	if err := persistAnnouncements(remaining); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"id": id})
}
