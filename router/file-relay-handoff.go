package router

import "github.com/gin-gonic/gin"

// APIDUO:file-relay:BEGIN
//
// handOffFileRetrieve lets the hailuo task plugin keep answering
// GET /v1/files/retrieve, the MiniMax wire path that shares its shape with a
// gateway-issued file id.
//
// It runs before relay token auth on purpose: the plugin route validates the
// caller with NativeRouteCredential, so a client that authenticates the vendor
// way must reach the plugin before the gateway token check rejects it. Any id
// that is not a plugin path falls through to the handler behind this one.
func (d *pluginRouteDispatcher) handOffFileRetrieve(c *gin.Context) {
	if c.Param("id") != "retrieve" {
		return
	}
	d.dispatch(c)
}

// APIDUO:file-relay:END
