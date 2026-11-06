package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

// APIDUO:file-relay:BEGIN
//
// /v1/files routes on purpose, not model, so it sits outside the model
// distributor: Distribute() rejects any request without a model name, which is
// exactly what an upload is.
//
// Auth is attached per route rather than to the group because the :id handoff
// has to run before TokenAuth: GET /v1/files/retrieve belongs to the hailuo task
// plugin, and that route validates the caller with the vendor credential.
func registerFileRelayRoutes(router *gin.Engine) {
	fileRouter := router.Group("/v1/files")
	fileRouter.Use(middleware.RouteTag("relay"))
	fileRouter.Use(middleware.SystemPerformanceCheck())
	{
		fileRouter.GET("/:id", defaultPluginDispatcher.handOffFileRetrieve, middleware.TokenAuth(), controller.FileRetrieve)
		fileRouter.DELETE("/:id", middleware.TokenAuth(), controller.FileDelete)
		fileRouter.GET("/:id/content", middleware.TokenAuth(), controller.FileContent)
		fileRouter.POST("", middleware.TokenAuth(), middleware.ModelRequestRateLimit(), controller.FileCreate)
		fileRouter.GET("", middleware.TokenAuth(), middleware.ModelRequestRateLimit(), controller.FileList)
	}
}

// APIDUO:file-relay:END
