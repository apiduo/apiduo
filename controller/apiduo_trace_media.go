package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/pkg/apiduo/trace"
	"github.com/gin-gonic/gin"
)

// GetApiduoTraceMedia serves one image out of the trace store.
//
// Public by design, and the only public endpoint in the trace feature. The
// URL is written into the span payload, and the Langfuse UI loads it from a
// different origin with no apiduo session — an authenticated route would show
// a broken image to every Langfuse user, which is exactly the audience this
// feature is for.
//
// The unguessable media id is the capability: 22 base64url characters minted
// by Langfuse per object, no listing endpoint, no way to enumerate. Same trust
// model as the presigned S3 link it replaces, minus the presigned expiry that
// would rot every old trace after an hour.
func GetApiduoTraceMedia(c *gin.Context) {
	id, ok := trace.ParseMediaParam(c.Param("media_id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "not found"})
		return
	}

	width := 0
	if raw := c.Query("w"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			width = n
		}
	}

	data, mime, err := trace.LoadMedia(id, width)
	switch {
	case errors.Is(err, trace.ErrMediaNotFound):
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "not found"})
		return
	case errors.Is(err, trace.ErrTraceDisabled):
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "not found"})
		return
	case err != nil:
		// Backend unreachable lives in the server log; the browser just gets
		// a status it can render as "preview unavailable".
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "media backend unavailable"})
		return
	}

	// Immutable: the id is derived from the bytes, so the same URL can never
	// answer with a different picture. The drawer's zoom reuses the full-size
	// URL after the thumbnail, and both hit this cache.
	c.Header("Cache-Control", "public, max-age=86400, immutable")
	c.Data(http.StatusOK, mime, data)
}
