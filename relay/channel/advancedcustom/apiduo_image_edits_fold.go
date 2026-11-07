package advancedcustom

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

/*
 * 「下游调 edits、上游只有 generations」的载体转换。
 *
 * 背景：edits 是 multipart 开的一个门，语义上和 generations 没有区别——图片既可以
 * 是一个文件，也可以是一段字符串引用（URL / data URI / 裸 base64）。而只提供
 * generations 的上游（火山方舟这类）收的是 JSON。原样把 multipart 打过去，上游
 * 只看到一堆边界符，回 400。
 *
 * 所以按 openai-image 协议的转换点 ① 做折叠：multipart → canonical JSON，
 * 图片与蒙版转成 data URI（不是裸 base64，裸串常被当成 URL 解析）。
 *
 * 五条规则（与 skill 的 §3.2 对齐，测试逐条固定）：
 *   1. 空 part = 没传，整个键不出现（HTML 未选文件会提交零长度 part）
 *   2. 多张图 → 数组；单张 → 字符串
 *   3. mime 按字节嗅探，客户端声明的 Content-Type 只在嗅探失败时兜底
 *   4. n 非法回落 1；没传就不写（缺省语义属于上游）
 *   5. 其余文本字段原样透传
 */

const (
	// 单张参考图上限。20 MiB 是「生成用参考图」的合理上界，再大多半是误传。
	apiduoImageFoldMaxFileBytes = 20 << 20
	// 整份请求上限：多图时按 4/3 的 base64 膨胀留余量。
	apiduoImageFoldMaxTotalBytes = 64 << 20
)

// shouldFoldEditsToJSON 判断这条路由要不要折叠。
//
// 只在「上游不是 edits 端点」时折叠：上游路径仍然是 */images/edits 的时候，
// 原生转发（multipart 原样发过去）才是对的，折了反而把喜欢 multipart 的上游弄坏。
func shouldFoldEditsToJSON(upstreamPath string) bool {
	trimmed := strings.TrimSpace(upstreamPath)
	if trimmed == "" {
		return false
	}
	return !strings.HasSuffix(strings.TrimRight(trimmed, "/"), "/images/edits")
}

// foldEditsRequestForJSONUpstream 把 multipart 的 edits 请求折成 JSON。
// 返回 (body, 是否已处理, error)：没触发折叠时 handled=false，调用方走原来的表单转发。
func foldEditsRequestForJSONUpstream(c *gin.Context, upstreamPath string) ([]byte, bool, error) {
	if !shouldFoldEditsToJSON(upstreamPath) {
		return nil, false, nil
	}
	form := c.Request.MultipartForm
	if form == nil {
		// 必须在改 Content-Type 之前解析：解析器依赖原始 multipart boundary。
		parsed, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return nil, true, fmt.Errorf("parse image edit form: %w", err)
		}
		form = parsed
	}

	body, err := foldEditsMultipart(form)
	if err != nil {
		return nil, true, err
	}
	return body, true, nil
}

func foldEditsMultipart(form *multipart.Form) ([]byte, error) {
	if form == nil {
		return nil, errors.New("image edit form is empty")
	}

	body := make(map[string]any, len(form.Value)+2)
	total := 0

	keys := make([]string, 0, len(form.Value)+len(form.File))
	for key := range form.Value {
		keys = append(keys, key)
	}
	for key := range form.File {
		if _, exists := form.Value[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys) // 输出稳定，方便测试与日志比对

	for _, key := range keys {
		field := strings.TrimSuffix(key, "[]")

		if field == "image" || field == "mask" {
			refs, used, err := foldImageReferences(form, key)
			if err != nil {
				return nil, err
			}
			total += used
			if total > apiduoImageFoldMaxTotalBytes {
				return nil, fmt.Errorf(
					"image edit payload exceeds %d bytes",
					apiduoImageFoldMaxTotalBytes,
				)
			}
			if len(refs) == 0 {
				continue // 空 part = 没传，键不出现
			}
			if field == "image" && len(refs) > 1 {
				body[field] = refs
			} else {
				body[field] = refs[0]
			}
			continue
		}

		values := form.Value[key]
		if len(values) == 0 {
			continue
		}
		raw := values[len(values)-1]
		if field == "n" {
			// 非法值回落 1，而不是把 "abc" 丢给上游赌它宽容。
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || n < 1 {
				n = 1
			}
			body[field] = n
			continue
		}
		body[field] = raw
	}

	return json.Marshal(body)
}

// foldImageReferences 收一个字段的图片引用：上传的文件转 data URI，
// 文本值（URL / data URI）原样收下。
func foldImageReferences(form *multipart.Form, key string) ([]string, int, error) {
	refs := make([]string, 0, 2)
	used := 0

	for _, header := range form.File[key] {
		if header == nil || header.Size <= 0 {
			continue // 零字节 = 没传
		}
		if header.Size > apiduoImageFoldMaxFileBytes {
			return nil, used, fmt.Errorf(
				"%s exceeds %d bytes",
				strings.TrimSuffix(key, "[]"),
				apiduoImageFoldMaxFileBytes,
			)
		}
		file, err := header.Open()
		if err != nil {
			return nil, used, fmt.Errorf("open %s: %w", key, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, apiduoImageFoldMaxFileBytes+1))
		_ = file.Close()
		if readErr != nil {
			return nil, used, fmt.Errorf("read %s: %w", key, readErr)
		}
		if len(data) == 0 {
			continue
		}
		mimeType, err := detectImageMimeType(data, header.Header.Get("Content-Type"))
		if err != nil {
			return nil, used, err
		}
		used += len(data)
		refs = append(refs, "data:"+mimeType+";base64,"+base64.StdEncoding.EncodeToString(data))
	}

	for _, value := range form.Value[key] {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		refs = append(refs, trimmed)
	}

	return refs, used, nil
}

// detectImageMimeType 先按字节嗅探，嗅探不出才信客户端声明的类型。
// 两者都不像图片就报错——把 application/octet-stream 塞给上游只会换一个更远的地方报错。
func detectImageMimeType(data []byte, declared string) (string, error) {
	sniffed := http.DetectContentType(data)
	if strings.HasPrefix(sniffed, "image/") {
		return sniffed, nil
	}
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(declared, ";", 2)[0]))
	if strings.HasPrefix(base, "image/") {
		return base, nil
	}
	return "", fmt.Errorf(
		"image field is not a recognised image (detected %q, declared %q)",
		sniffed,
		declared,
	)
}
