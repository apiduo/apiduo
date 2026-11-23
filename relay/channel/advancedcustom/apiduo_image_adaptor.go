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
	// APIDUO:primary-endpoint:BEGIN
	// 渠道主端点是收敛契约：是图片路径就以它为准，primary_path 只在它缺席时兜底。
	cfg = cfg.WithChannelPrimary(info.ChannelOtherSettings.PrimaryEndpoint)
	// APIDUO:primary-endpoint:END

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

	case relayconstant.RelayModeResponses:
		// 直接折成 canonical，不经过 chat：绕一层会把 input_image 的 data URI
		// 全量复制一遍，也会带上与图片无关的工具调用语义。
		request, ok := info.Request.(*dto.OpenAIResponsesRequest)
		if !ok {
			return nil, true, errors.New("responses image protocol requires a responses request")
		}
		a.imageStream = request.Stream != nil && *request.Stream
		canonical, err = canonicalFromResponses(request, info.UpstreamModelName)

	case relayconstant.RelayModeGemini:
		// 判据只有一条：渠道开着图片协议（ServesPath 已经把这条路径算作它的入口，
		// 不再需要 advanced_routes 声明）。开了就是图片渠道，与 chat 入口同一套语义，
		// 不看 body，也不查全局模型名单——名单是 chat 转换决定 responseModalities 用的，
		// 不是渠道能力判据，放这里会让「渠道配好、名单漏加」变成静默不出图。
		request, ok := info.Request.(*dto.GeminiChatRequest)
		if !ok {
			return nil, true, errors.New("gemini image protocol requires a generateContent request")
		}
		canonical, err = canonicalFromGemini(request, info.UpstreamModelName)

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

	plan, err := planImageRequestWithModel(canonical, cfg, info.UpstreamModelName)
	if err != nil {
		return nil, true, err
	}
	if strings.TrimSpace(plan.UpstreamPath) == "" {
		return nil, true, errors.New("image protocol resolved an empty upstream path")
	}

	a.imageProtocolHandled = true
	a.imageClientModel = canonical.model()
	// 归一化之后只剩两个值，所以这里等价于「客户端要的不是 b64」。
	a.imageWantURL = canonical.fields["response_format"] != "b64_json"
	// Gemini 入口只认 inlineData：这个入口的载体不能换成链接。
	if info.RelayMode == relayconstant.RelayModeGemini {
		a.imageWantURL = false
	}
	a.imageUpstreamPath = plan.UpstreamPath
	a.imageRequestCount = 1
	// canonical 的字段来自 JSON 解码，number 一律是 float64；只有直接读表单的
	// 路径才会是 int。只认 int 会让 /v1/images/generations 的 n 恒为 1，多图按一张计费。
	// 注意 chat / responses / Gemini 原生三个入口的 n 不参与计费：它们的折叠函数
	// 刻意不搬 n（对话参数不该混进图像请求），所以这里的张数恒为 1。
	switch n := canonical.fields["n"].(type) {
	case int:
		if n >= 1 {
			a.imageRequestCount = n
		}
	case float64:
		if n >= 1 {
			a.imageRequestCount = int(n)
		}
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
//
// 接管了但地址构建不出来时返回 error，而不是退回路由表的地址：那会让「配置写错了」
// 表现成「请求成功但发去了另一个端点」，比直接失败更难查。
func (a *Adaptor) imageProtocolURL(info *relaycommon.RelayInfo) (string, bool, error) {
	if a.imageUpstreamPath == "" {
		return "", false, nil
	}
	route := a.route
	route.UpstreamPath = a.imageUpstreamPath
	target, err := buildRouteURL(route, a.converter, info, "", "")
	if err != nil {
		return "", true, err
	}
	return target, true, nil
}

// doImageProtocolResponse 是 DoResponse 挂接块的全部实现。
func (a *Adaptor) doImageProtocolResponse(
	c *gin.Context,
	resp *http.Response,
	info *relaycommon.RelayInfo,
) (any, *types.NewAPIError, bool) {
	// 入口只负责「按入口回包」，不替业务判定成败。上游 200 + 空 data[]
	// 时 chat 仍回一个 content 齐备的 chat.completion，images 入口原样透传。
	if !a.imageProtocolHandled {
		return nil, nil, false
	}
	if info.RelayMode == relayconstant.RelayModeChatCompletions {
		usage, apiErr := finishImageChatResponse(
			c, resp, info, a.imageClientModel, a.imageRequestCount, a.imageStream, a.imageWantURL,
		)
		return usage, apiErr, true
	}
	if info.RelayMode == relayconstant.RelayModeGemini {
		usage, apiErr := finishImageGeminiResponse(c, resp, info, a)
		return usage, apiErr, true
	}
	if info.RelayMode == relayconstant.RelayModeResponses {
		usage, apiErr := finishImageResponsesResponse(c, resp, info, a)
		return usage, apiErr, true
	}
	// 不再因「没配存储」放行上游原样透传：那会把上游附加字段与错误页直接
	// 交给客户端。没有存储时把 b64 原样重建回去，仍然经过白名单。
	// 体积上限在读取处按 64 MiB 截断：Content-Length 可能是 -1（chunked），
	// 用长度预判会把合法响应判成失败。
	usage, apiErr := finishImageNativeResponse(c, resp, info, a)
	return usage, apiErr, true
}
