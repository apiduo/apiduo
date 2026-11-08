// Playground 的多模态入口：生图与生视频。
//
// 放在独立文件而不是追加进上游 playground.go，是为了不抬高合并面——
// 上游文件里每多一行自有代码，未来 rebase 就多一处冲突。这里只导出
// 三个 handler，路由的挂接留在 relay-router.go 的标记块内（那处不可替代）。
package controller

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// setupPlaygroundRelay 复刻上游 Playground 的前置处理：拒绝 access token、
// 生成 relay info、写用户缓存上下文、造临时令牌。
// 三个模态共用，避免各自漏一步导致计费按错分组。
func setupPlaygroundRelay(c *gin.Context, format types.RelayFormat) *types.NewAPIError {
	if c.GetBool("use_access_token") {
		return types.NewError(errors.New(i18n.T(c, i18n.MsgRelayAccessTokenUnsupported)), types.ErrorCodeAccessDenied, types.ErrOptionWithSkipRetry())
	}

	relayInfo, err := relaycommon.GenRelayInfo(c, format, nil, nil)
	if err != nil {
		return types.NewError(err, types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	userId := c.GetInt("id")
	userCache, err := model.GetUserCache(userId)
	if err != nil {
		return types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
	}
	userCache.WriteContext(c)

	tempToken := &model.Token{
		UserId: userId,
		Name:   fmt.Sprintf("playground-%s", relayInfo.UsingGroup),
		Group:  relayInfo.UsingGroup,
	}
	_ = middleware.SetupContextForToken(c, tempToken)
	return nil
}

func respondPlaygroundError(c *gin.Context, newAPIError *types.NewAPIError) {
	c.JSON(newAPIError.StatusCode, gin.H{
		"error": newAPIError.ToOpenAIError(common.UpstreamAddresses(c)...),
	})
}

// PlaygroundImage 走图像 relay：请求体与对外 /v1/images/generations 一致，
// 计费按张数 × 单价，与正常调用同一套规则。
//
// relay_mode 必须显式给：distributor 的 Path2RelayMode 只认 /v1 前缀，
// /pg/images/generations 会落到 RelayModeUnknown，进而被当成文本请求处理
// （报 "expected dto.GeneralOpenAIRequest, got *dto.ImageRequest"）。
func PlaygroundImage(c *gin.Context) {
	c.Set("relay_mode", relayconstant.RelayModeImagesGenerations)
	if newAPIError := setupPlaygroundRelay(c, types.RelayFormatOpenAIImage); newAPIError != nil {
		respondPlaygroundError(c, newAPIError)
		return
	}
	Relay(c, types.RelayFormatOpenAIImage)
}

// PlaygroundVideo 走异步任务链路：返回任务 id，前端再轮询 PlaygroundVideoFetch。
func PlaygroundVideo(c *gin.Context) {
	c.Set("relay_mode", relayconstant.RelayModeVideoSubmit)
	if newAPIError := setupPlaygroundRelay(c, types.RelayFormatTask); newAPIError != nil {
		respondPlaygroundError(c, newAPIError)
		return
	}
	RelayTask(c)
}

// PlaygroundVideoFetch 查询任务结果，结算在任务到达终态时由 task_billing 完成。
func PlaygroundVideoFetch(c *gin.Context) {
	c.Set("relay_mode", relayconstant.RelayModeVideoFetchByID)
	if newAPIError := setupPlaygroundRelay(c, types.RelayFormatTask); newAPIError != nil {
		respondPlaygroundError(c, newAPIError)
		return
	}
	RelayTaskFetch(c)
}
