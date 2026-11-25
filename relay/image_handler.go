package relay

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	// APIDUO:image-usage-estimate:BEGIN
	"github.com/QuantumNous/new-api/pkg/imageusage"
	// APIDUO:image-usage-estimate:END
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/requestplugin"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/gin-gonic/gin"
)

// APIDUO:image-usage-estimate:BEGIN
// hasUpstreamImageUsage reports whether an adaptor's usage came from the
// upstream response rather than from its single-unit placeholder. A real image
// request never costs one token, so {prompt:1, completion:0, total:1} means the
// upstream reported nothing.
func hasUpstreamImageUsage(usage *dto.Usage) bool {
	if usage == nil {
		return false
	}
	return usage.PromptTokens > 1 || usage.CompletionTokens > 0 || usage.TotalTokens > 1
}

// APIDUO:image-usage-estimate:END

// outboundImageCounter is an image adaptor whose upstream body carries the
// image count in a member other than n. OutboundImageCount reads the count
// from that body, nil when the member is absent.
type outboundImageCounter interface {
	OutboundImageCount(body []byte) (*uint, error)
}

func ImageHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {
	info.InitChannelMeta(c)
	info.BillingImageCount = nil
	info.ImageRequestCount = 0
	// APIDUO:image-usage-estimate:BEGIN
	info.EstimatedImageUsage = nil
	info.EstimatedImageCount = 0
	info.ImageUsageEstimated = false
	// APIDUO:image-usage-estimate:END

	imageReq, ok := info.Request.(*dto.ImageRequest)
	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected dto.ImageRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	request, err := common.DeepCopy(imageReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to ImageRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)
	imageCount, err := request.ImageCount(false)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	var requestBody io.Reader
	var jsonData []byte
	var outbound struct {
		N *uint `json:"n"`
	}
	validateQuantity := false

	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		if newAPIError = requestplugin.PassThrough(c, info, storage); newAPIError != nil {
			return newAPIError
		}
		if !strings.Contains(c.Request.Header.Get("Content-Type"), "multipart/form-data") {
			counter, countsOwnMember := adaptor.(outboundImageCounter)
			switch {
			case countsOwnMember:
				// The adaptor sends the count in its own member, which only
				// it can read, so it reads the whole body.
				var raw []byte
				raw, err = storage.Bytes()
				if err == nil {
					err = common.Unmarshal(raw, &outbound)
				}
				if err == nil {
					outbound.N, err = counter.OutboundImageCount(raw)
				}
			case storage.IsDisk():
				reader, readErr := storage.NewReader()
				if readErr != nil {
					return types.NewErrorWithStatusCode(readErr, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
				}
				err = common.DecodeJsonBody(reader, &outbound)
				_ = reader.Close()
			default:
				var raw []byte
				raw, err = storage.Bytes()
				if err == nil {
					err = common.Unmarshal(raw, &outbound)
				}
			}
			if err != nil {
				return types.NewErrorWithStatusCode(fmt.Errorf("invalid image billing parameters: %w", err), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
			}
			validateQuantity = true
			if common.DebugEnabled {
				if raw, readErr := storage.Bytes(); readErr == nil {
					logger.LogDebug(c, "image request body: %s", raw)
				}
			}
		}
		requestBody = common.NewReplayableBodyReader(storage)
	} else {
		convertedRequest, err := adaptor.ConvertImageRequest(c, info, *request)
		if err != nil {
			// An adaptor that already classified its rejection (status code
			// and retry policy) keeps that classification instead of being
			// downgraded to a retryable conversion failure.
			var apiErr *types.NewAPIError
			if errors.As(err, &apiErr) {
				return apiErr
			}
			return newConvertRequestFailedError(c, info, err)
		}
		relaycommon.AppendRequestConversionFromRequest(info, convertedRequest)

		switch convertedRequest.(type) {
		case *bytes.Buffer:
			if newAPIError = requestplugin.PassThrough(c, info, nil); newAPIError != nil {
				return newAPIError
			}
			requestBody = convertedRequest.(io.Reader)
		default:
			jsonData, err = common.Marshal(convertedRequest)
			if err != nil {
				return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
			}

			if jsonData, newAPIError = requestplugin.Outbound(c, info, jsonData); newAPIError != nil {
				return newAPIError
			}
			// apply param override
			if len(info.ParamOverride) > 0 {
				jsonData, err = relaycommon.ApplyParamOverrideWithRelayInfo(jsonData, info)
				if err != nil {
					return newAPIErrorFromParamOverride(err)
				}
			}
			if newAPIError = requestplugin.Egress(c, info, jsonData); newAPIError != nil {
				return newAPIError
			}

		}
	}
	if jsonData != nil {
		// This is a different trust boundary from ingress: channel overrides
		// and request plugins can change the quantity actually submitted, in
		// n or in the member the adaptor sends it in.
		err := common.Unmarshal(jsonData, &outbound)
		if counter, ok := adaptor.(outboundImageCounter); ok && err == nil {
			outbound.N, err = counter.OutboundImageCount(jsonData)
		}
		if err != nil {
			return types.NewErrorWithStatusCode(fmt.Errorf("invalid image billing parameters: %w", err), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		validateQuantity = true
	}
	if validateQuantity {
		// Validate the count in the body sent upstream, including channel overrides.
		quantityRequest := dto.ImageRequest{N: outbound.N}
		if quantityRequest.N == nil {
			quantityRequest.N = common.GetPointer(uint(imageCount))
		}
		imageCount, err = quantityRequest.ImageCount(false)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
	}
	if jsonData != nil {
		logger.LogDebug(c, "image request body: %s", jsonData)
		body, closer, err := relaycommon.NewOutboundJSONBody(jsonData)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		defer closer.Close()
		requestBody = body
	}

	// APIDUO:image-usage-estimate:BEGIN
	// The estimate is computed before the request so an adaptor that gets no
	// usage from upstream can put the same numbers in the response body it
	// writes and in the usage it returns for billing. A channel alias may hide
	// the model family, so fall back to the public name when the upstream one
	// has no table.
	estimateModel := request.Model
	if !imageusage.Supported(estimateModel) {
		estimateModel = info.OriginModelName
	}
	if estimate, ok := imageusage.ForRequest(
		estimateModel,
		request.Prompt,
		request.Size,
		request.Quality,
		imageCount,
		imageusage.CountInputImages(request.Images),
	); ok {
		info.EstimatedImageUsage = estimate.Usage()
		info.EstimatedImageCount = imageCount
	}
	// APIDUO:image-usage-estimate:END

	if billingErr := service.PrepareImageBillingForRequest(c, info, imageCount); billingErr != nil {
		return billingErr
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")

	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	var httpResp *http.Response
	if resp != nil {
		httpResp = resp.(*http.Response)
		info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
		if httpResp.StatusCode != http.StatusOK {
			if httpResp.StatusCode == http.StatusCreated && info.ApiType == constant.APITypeReplicate {
				// replicate channel returns 201 Created when using Prefer: wait, treat it as success.
				httpResp.StatusCode = http.StatusOK
			} else {
				newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
				// reset status code 重置状态码
				service.ResetStatusCode(newAPIError, statusCodeMappingStr)
				return newAPIError
			}
		}
	}

	// APIDUO:artifact-rewrite:BEGIN
	// Copy every media reference in the response into object storage and hand
	// back the stored addresses. A stream, an oversized body, an unconfigured
	// channel or any storage failure leaves the response exactly as the upstream
	// produced it - storage must never turn a good answer into a bad one.
	//
	// Image-protocol channels are skipped: the adaptor decodes their body right
	// after this point and links images itself (alignImageCarriers).
	if !info.ArtifactsHandledByAdaptor() {
		httpResp = service.RewriteResponseArtifactsInPlace(c.Request.Context(), info.GetChannelID(), httpResp)
	}
	// APIDUO:artifact-rewrite:END

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	// APIDUO:image-usage-estimate:BEGIN
	// An upstream that reports nothing leaves the adaptor's single-unit
	// placeholder. Settle on the local lower-bound estimate instead, so a
	// token-priced image model is not billed one token per image.
	if usageObj, ok := usage.(*dto.Usage); ok {
		if info.EstimatedImageUsage != nil && !hasUpstreamImageUsage(usageObj) {
			usage = info.EstimatedImageUsage
			info.ImageUsageEstimated = true
		}
	} else if info.EstimatedImageUsage != nil {
		usage = info.EstimatedImageUsage
		info.ImageUsageEstimated = true
	} else {
		usage = &dto.Usage{}
	}
	// APIDUO:image-usage-estimate:END

	// The log content shows the settled count: the count the handler derived
	// from the upstream response when it did, otherwise the reserved quantity.
	imageN := info.RequestedImageCount()
	if info.BillingImageCount != nil {
		imageN = *info.BillingImageCount
	} else if count, ok := info.PriceData.OtherRatios()["n"]; ok && info.PriceData.UsePrice && count >= 1 && count <= dto.MaxImageN {
		imageN = common.QuotaRound(count)
	}

	if usage.(*dto.Usage).TotalTokens == 0 {
		usage.(*dto.Usage).TotalTokens = 1
	}
	if usage.(*dto.Usage).PromptTokens == 0 {
		usage.(*dto.Usage).PromptTokens = 1
	}

	// APIDUO:image-log-quality:BEGIN
	// No default: "standard" is a DALL-E word, gpt-image defaults to "auto",
	// and Gemini image requests have no quality at all. Printing one would
	// report a tier the upstream never received.
	quality := request.Quality
	// APIDUO:image-log-quality:END

	var logContent []*common.Message

	if len(request.Size) > 0 {
		logContent = append(logContent, common.NewMessage("Size {{size}}", map[string]any{"size": request.Size}))
	}
	if len(quality) > 0 {
		logContent = append(logContent, common.NewMessage("Quality {{quality}}", map[string]any{"quality": quality}))
	}
	if imageN > 0 {
		logContent = append(logContent, common.NewMessage("Image count {{count}}", map[string]any{"count": imageN}))
	}

	service.PostTextConsumeQuota(c, info, usage.(*dto.Usage), logContent)
	return nil
}
