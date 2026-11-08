package advancedcustom

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

/*
 * 图像协议在 advancedcustom adaptor 里的唯一入口。
 *
 * 三个下游入口都在这里折叠成 canonical，再由脚本决定上游端点与载体。
 * 不改 TextHelper：调研确认响应形状完全由 adaptor.DoResponse 掌控，
 * 所以 chat 入口也能在 adaptor 内闭环，无需动上游 relay 分发。
 *
 * 默认关闭：没有 image_protocol 配置时这个函数一律返回 handled=false，
 * 原有链路一个字节都不变。
 */

// prepareImageProtocol 返回 (plan, 是否接管, error)。
func (a *Adaptor) prepareImageProtocol(
	c *gin.Context,
	info *relaycommon.RelayInfo,
) (*imagePlan, bool, error) {
	if info == nil || c == nil {
		return nil, false, nil
	}
	cfg := info.ChannelOtherSettings.ImageProtocol
	if cfg == nil || !cfg.Enabled {
		return nil, false, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, true, err
	}

	var canonical *imageCanonical
	var err error

	switch info.RelayMode {
	case relayconstant.RelayModeChatCompletions:
		request, ok := info.Request.(*dto.GeneralOpenAIRequest)
		if !ok {
			return nil, true, errors.New("chat image protocol requires a chat completions request")
		}
		// stream 是入口层的传输开关，不是「上游不支持」。上游同步返回
		// 完整图片，回包时按入口拼成 chat.completion.chunk 的 SSE 即可。
		a.imageStream = request.Stream != nil && *request.Stream
		canonical, err = canonicalFromChat(request)

	case relayconstant.RelayModeImagesEdits:
		// edits 只接受文件流：JSON 载体没有文件本体（image 只能是 URL / data URI /
		// base64 文本），接受它等于把「文件编辑」降级成「网关代取图」。直接 400，
		// 让客户端改成 multipart 上传，或改走 generations / chat 入口。
		err = requireFileStreamEdits(c)
		if err == nil {
			var form = c.Request.MultipartForm
			if form == nil {
				form, err = common.ParseMultipartFormReusable(c)
			}
			if err == nil {
				canonical, err = canonicalFromForm(form)
			}
		}

	case relayconstant.RelayModeImagesGenerations:
		var raw []byte
		raw, err = readOriginalBody(c)
		if err == nil {
			canonical, err = canonicalFromJSON(raw)
		}

	default:
		// 其它入口（embeddings / rerank / audio …）不受图像协议影响。
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if err := canonical.normalize(); err != nil {
		return nil, true, err
	}

	plan, err := planImageRequest(canonical, cfg)
	if err != nil {
		return nil, true, err
	}
	if strings.TrimSpace(plan.UpstreamPath) == "" {
		return nil, true, errors.New("image protocol resolved an empty upstream path")
	}

	a.imageProtocolHandled = true
	a.imageClientModel = canonical.model()
	a.imageUpstreamPath = plan.UpstreamPath
	a.imageRequestCount = 1
	if n, ok := canonical.fields["n"].(int); ok && n >= 1 {
		a.imageRequestCount = n
	}
	return plan, true, nil
}

// requireFileStreamEdits 守住 edits 这扇门：OpenAI 的 /v1/images/edits 只接受
// multipart/form-data 的文件流。JSON 请求体里没有文件本体，只有文本引用，
// 走这条路会让「编辑这张图」退化成「网关替客户端下载 URL」，所以一律 400。
func requireFileStreamEdits(c *gin.Context) error {
	if c == nil || c.Request == nil {
		return errors.New("/v1/images/edits requires a multipart/form-data body")
	}
	contentType := strings.TrimSpace(c.Request.Header.Get("Content-Type"))
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		return nil
	}
	return fmt.Errorf(
		"/v1/images/edits accepts multipart/form-data file uploads only, got %q; "+
			"upload the image as a file part, or use /v1/images/generations for URL / base64 references",
		contentType,
	)
}

// rejectTextImageFields 扫表单里的 image / mask：非空文本值一律拒绝。
// 空值不算（HTML 未选文件会提交一个空 part）。
func rejectTextImageFields(form *multipart.Form) error {
	if form == nil {
		return nil
	}
	for key, values := range form.Value {
		field, _ := imageFieldName(key)
		if field != "image" && field != "mask" {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				continue
			}
			return fmt.Errorf(
				"%s must be uploaded as a file part; /v1/images/edits does not accept text references (URL / data URI / base64)",
				field,
			)
		}
	}
	return nil
}

// guardFileStreamEdits 是 ConvertImageRequest 阶段的前置校验。
//
// 必须早于 openai 的转换器：它遇到「multipart 里没有文件」会先报 image is required
// （100% 会被记成可重试的 500），混传文件 + 文本 image 时还会把文本原样写回上游表单。
//
// 覆盖所有 advancedcustom 渠道，不只是启用 image_protocol 的：实测未启用时，
// multipart 文本引用 → 500 image is required，urlencoded 文本引用 → 200 并把 URL 原样
// 转发给上游。同一个入口三种载体三种结果，比直接 400 更难查。
func guardFileStreamEdits(c *gin.Context, info *relaycommon.RelayInfo) error {
	if c == nil || info == nil || info.RelayMode != relayconstant.RelayModeImagesEdits {
		return nil
	}
	if err := requireFileStreamEdits(c); err != nil {
		return editsFileStreamError(err)
	}

	form := c.Request.MultipartForm
	if form == nil {
		parsed, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return editsFileStreamError(fmt.Errorf("parse images/edits form: %w", err))
		}
		// 存回去让后面的转换器复用，20 MiB 的图不用解析两遍。
		c.Request.MultipartForm = parsed
		c.Request.PostForm = url.Values(parsed.Value)
		form = parsed
	}
	if err := rejectTextImageFields(form); err != nil {
		return editsFileStreamError(err)
	}
	return nil
}

// editsFileStreamError 把载体拒绝标成 400 且不重试：客户端换个载体就能过，
// 重试同一个坏请求只会一直 400。
func editsFileStreamError(err error) error {
	return types.NewOpenAIError(
		err,
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
}

func readOriginalBody(c *gin.Context) ([]byte, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	raw, err := storage.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("image request body is empty")
	}
	return raw, nil
}

// doImageProtocolRequest 是 DoRequest 挂接块的全部实现。
// 挂接块里只留一次调用，业务逻辑一行都不留在上游文件里。
func (a *Adaptor) doImageProtocolRequest(
	c *gin.Context,
	info *relaycommon.RelayInfo,
) (any, bool, error) {
	plan, handled, err := a.prepareImageProtocol(c, info)
	if !handled {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, types.NewOpenAIError(
			err,
			types.ErrorCodeInvalidRequest,
			http.StatusBadRequest,
			types.ErrOptionWithSkipRetry(),
		)
	}

	// 脚本可能把 multipart 换成 JSON（或反过来）。发完立刻还原：
	// 重试会用同一个 gin.Context 再走一遍 DoRequest。
	originalContentType := c.Request.Header.Get("Content-Type")
	c.Request.Header.Set("Content-Type", plan.ContentType)
	resp, requestErr := channel.DoApiRequest(a, c, info, bytes.NewReader(plan.Body))
	c.Request.Header.Set("Content-Type", originalContentType)
	return resp, true, requestErr
}

// imageProtocolURL 返回脚本选定的端点；没接管时第二个返回值为 false。
func (a *Adaptor) imageProtocolURL(info *relaycommon.RelayInfo) (string, bool) {
	if a.imageUpstreamPath == "" {
		return "", false
	}
	route := a.route
	route.UpstreamPath = a.imageUpstreamPath
	target, err := buildRouteURL(route, a.converter, info)
	if err != nil {
		return "", false
	}
	return target, true
}

// doImageProtocolResponse 是 DoResponse 挂接块的全部实现。
func (a *Adaptor) doImageProtocolResponse(
	c *gin.Context,
	resp *http.Response,
	info *relaycommon.RelayInfo,
) (any, *types.NewAPIError, bool) {
	// 入口只负责「按入口回包」，不替业务判定成败。上游 200 + 空 data[]
	// 时 chat 仍回一个 content 齐备的 chat.completion，images 入口原样透传。
	// 不读 body、不改 resp.Body，正常路径与上游零差异。
	if !a.imageProtocolHandled || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return nil, nil, false
	}
	usage, apiErr := finishImageChatResponse(
		c, resp, info, a.imageClientModel, a.imageRequestCount, a.imageStream,
	)
	return usage, apiErr, true
}
