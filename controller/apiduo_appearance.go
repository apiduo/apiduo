package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

// GetApiduoAppearance answers with the decoration apiduo paints on its own
// surfaces.
//
// Public on purpose: the sign-in page and the console shell both render apiduo
// surfaces before a session exists, and the payload is a colour plus three
// numbers. Nothing here describes a business rule an anonymous visitor could
// exploit.
func GetApiduoAppearance(c *gin.Context) {
	common.ApiSuccess(c, system_setting.GetApiduoAppearanceSetting().Snapshot())
}
