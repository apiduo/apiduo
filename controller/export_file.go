package controller

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// DownloadExportFile 提供账单与导出文件的下载。
//
// 两种进入方式：
//  1. 带 `access` 签名（邮件与短信里的链接），不要求登录；凭据绑定 key 与
//     过期时间，泄露一个链接不等于泄露整个目录。
//  2. 管理员会话（后台直接下载）。普通用户即使已登录也必须走签名 ——
//     否则他凭自己的会话就能猜别人的 key 拿走别人的账单。
//
// Range / If-Modified-Since 交给 http.ServeFile，几 MB 的 xlsx 也能断点续传。
func DownloadExportFile(c *gin.Context) {
	key := strings.TrimPrefix(c.Param("key"), "/")
	if key == "" {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	if !service.VerifyExportFileAccess(c.Query("access"), key) {
		if c.GetInt("role") < common.RoleAdminUser {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}

	path, info, err := service.ExportFileStat(key)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrExportKeyInvalid):
			c.AbortWithStatus(http.StatusBadRequest)
		case errors.Is(err, service.ErrExportFileMissing):
			c.AbortWithStatus(http.StatusNotFound)
		default:
			common.SysError(common.LogText("failed to open export file %q: %s", key, err.Error()))
			c.AbortWithStatus(http.StatusInternalServerError)
		}
		return
	}

	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	c.Header("ETag", service.ExportFileETag(info))
	c.Header("Cache-Control", "private, no-cache")
	c.File(path)
}
