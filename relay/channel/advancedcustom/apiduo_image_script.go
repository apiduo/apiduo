package advancedcustom

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

/*
 * 上游脚本层（转换点 ③）：canonical → 某个上游的端点 + 载体 + 编码。
 *
 * 一个上游一个脚本。配置面只有主端点，从端点由脚本推导（§3.4.1）：
 * 末段替换，scheme / host / query 全部保留。推不出来就报配置错，不猜路径。
 *
 * 载体是四个维度里唯一可能触发 IO 的（URL 要下载才能内联），所以留在这一层，
 * 不放进纯函数折叠层。默认不产生任何出站连接。
 */

const (
	imageURLFetchTimeout = 10 * time.Second
	imageMaxRefBytes     = 20 << 20
)

type imagePlan struct {
	UpstreamPath string
	ContentType  string
	Body         []byte
	ImageCount   int
}

func planImageRequest(c *imageCanonical, cfg *dto.ImageProtocolConfig) (*imagePlan, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, errors.New("image protocol is not enabled")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch cfg.Upstream {
	case dto.ImageUpstreamSeedream:
		return planSeedream(c, cfg)
	case dto.ImageUpstreamGPTImage2:
		return planGPTImage2(c, cfg)
	default:
		return nil, fmt.Errorf("unknown image upstream %q", cfg.Upstream)
	}
}

// planSeedream：单端点，带 image 即图生图。image 保持 str | list<str>，
// JSON 编码。skill §3.7：seedream 的 mask 不转发；size 档位换算属方言，
// 这里不擅自改写（会静默改变语义），留给显式配置。
func planSeedream(c *imageCanonical, cfg *dto.ImageProtocolConfig) (*imagePlan, error) {
	upstream := c.clone()
	upstream.drop("mask")

	// 裸 base64 不是合法引用：补上 data URI 前缀，否则上游当 URL 解析并报
	// invalid url specified（skill §3.5 实证）。
	if refs := upstream.imageRefs(); len(refs) > 0 {
		normalised := make([]string, len(refs))
		for i, ref := range refs {
			normalised[i] = inlineBareBase64(ref)
		}
		// 保留客户端给的形状：长度 1 的数组仍是数组（str | list<str> 都收）。
		switch upstream.fields["image"].(type) {
		case []string, []any:
			upstream.fields["image"] = normalised
		default:
			upstream.fields["image"] = normalised[0]
		}
	}

	body, err := upstream.bytes()
	if err != nil {
		return nil, err
	}
	return &imagePlan{
		UpstreamPath: cfg.Primary(),
		ContentType:  "application/json",
		Body:         body,
		ImageCount:   len(c.imageRefs()),
	}, nil
}

// planGPTImage2：文生图走主端点（不收 image）；图生图走从端点，编码成 multipart 文件。
func planGPTImage2(c *imageCanonical, cfg *dto.ImageProtocolConfig) (*imagePlan, error) {
	if !c.hasImage() {
		upstream := c.clone()
		upstream.drop("image", "mask")
		body, err := upstream.bytes()
		if err != nil {
			return nil, err
		}
		return &imagePlan{
			UpstreamPath: cfg.Primary(),
			ContentType:  "application/json",
			Body:         body,
		}, nil
	}

	editPath := strings.TrimSpace(cfg.EditPath)
	if editPath == "" {
		var err error
		editPath, err = deriveEditPath(cfg.Primary())
		if err != nil {
			return nil, err
		}
	}

	refs := c.imageRefs()
	images := make([]imageBlob, 0, len(refs))
	for _, ref := range refs {
		blob, err := resolveImageRef(ref, cfg)
		if err != nil {
			return nil, fmt.Errorf("image reference: %w", err)
		}
		images = append(images, blob)
	}

	var mask *imageBlob
	if raw, exists := c.fields["mask"]; exists {
		if ref, ok := raw.(string); ok {
			blob, err := resolveImageRef(ref, cfg)
			if err != nil {
				return nil, fmt.Errorf("mask reference: %w", err)
			}
			mask = &blob
		}
	}

	body, contentType, err := encodeImageForm(c, images, mask)
	if err != nil {
		return nil, err
	}
	return &imagePlan{
		UpstreamPath: editPath,
		ContentType:  contentType,
		Body:         body,
		ImageCount:   len(images),
	}, nil
}

// deriveEditPath 末段替换 /generations → /edits，query 原样保留。
// 末段不是 generations 就报错，不猜路径。
func deriveEditPath(primary string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(primary))
	if err != nil {
		return "", fmt.Errorf("image_protocol.primary_path is invalid: %w", err)
	}
	segments := strings.Split(strings.TrimRight(parsed.Path, "/"), "/")
	if len(segments) == 0 || segments[len(segments)-1] != "generations" {
		return "", fmt.Errorf(
			"cannot derive the image edit endpoint from %q: set image_protocol.edit_path",
			primary,
		)
	}
	segments[len(segments)-1] = "edits"
	parsed.Path = strings.Join(segments, "/")
	return parsed.String(), nil
}

type imageBlob struct {
	Data     []byte
	MimeType string
	Name     string
}

// resolveImageRef 把三种引用统一成字节。data URI 与裸 base64 是本地解码，
// http(s) 需要显式开关，且拒绝私网地址。
func resolveImageRef(ref string, cfg *dto.ImageProtocolConfig) (imageBlob, error) {
	ref = strings.TrimSpace(ref)

	if strings.HasPrefix(ref, "data:") {
		return decodeDataURI(ref)
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		if cfg == nil || !cfg.AllowURLFetch {
			return imageBlob{}, errors.New(
				"an http(s) image reference needs to be downloaded, but image_protocol.allow_url_fetch is off; send a data URI or enable it",
			)
		}
		return fetchImageRef(ref)
	}
	if decoded, err := base64.StdEncoding.DecodeString(ref); err == nil && len(decoded) > 0 {
		mimeType, err := detectImageMimeType(decoded, "")
		if err != nil {
			return imageBlob{}, err
		}
		return imageBlob{Data: decoded, MimeType: mimeType, Name: "image"}, nil
	}
	return imageBlob{}, fmt.Errorf("unrecognised image reference %q", truncateForLog(ref))
}

func decodeDataURI(ref string) (imageBlob, error) {
	comma := strings.IndexByte(ref, ',')
	if comma < 0 || !strings.HasSuffix(ref[:comma], ";base64") {
		return imageBlob{}, errors.New("data URI must be base64 encoded")
	}
	mimeType := strings.TrimSuffix(strings.TrimPrefix(ref[:comma], "data:"), ";base64")
	decoded, err := base64.StdEncoding.DecodeString(ref[comma+1:])
	if err != nil {
		return imageBlob{}, fmt.Errorf("invalid data URI: %w", err)
	}
	if len(decoded) == 0 {
		return imageBlob{}, errors.New("data URI is empty")
	}
	if len(decoded) > imageMaxRefBytes {
		return imageBlob{}, fmt.Errorf("image exceeds %d bytes", imageMaxRefBytes)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		sniffed, sniffErr := detectImageMimeType(decoded, mimeType)
		if sniffErr != nil {
			return imageBlob{}, sniffErr
		}
		mimeType = sniffed
	}
	return imageBlob{Data: decoded, MimeType: mimeType, Name: "image"}, nil
}

func fetchImageRef(ref string) (imageBlob, error) {
	parsed, err := url.Parse(ref)
	if err != nil || parsed.Hostname() == "" {
		return imageBlob{}, fmt.Errorf("invalid image URL %q", truncateForLog(ref))
	}
	if host := parsed.Hostname(); isPrivateHost(host) {
		return imageBlob{}, fmt.Errorf("image URL host %q is not allowed", host)
	}

	client := &http.Client{Timeout: imageURLFetchTimeout}
	resp, err := client.Get(ref)
	if err != nil {
		return imageBlob{}, fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return imageBlob{}, fmt.Errorf("download image: upstream returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, imageMaxRefBytes+1))
	if err != nil {
		return imageBlob{}, fmt.Errorf("read image: %w", err)
	}
	if len(data) > imageMaxRefBytes || len(data) == 0 {
		return imageBlob{}, fmt.Errorf("image exceeds %d bytes or is empty", imageMaxRefBytes)
	}
	mimeType, err := detectImageMimeType(data, resp.Header.Get("Content-Type"))
	if err != nil {
		return imageBlob{}, err
	}
	return imageBlob{Data: data, MimeType: mimeType, Name: "image"}, nil
}

// isPrivateHost 是最小 SSRF 防线：内网/回环地址一律拒。
// 只查 IP 字面量等于给 DNS 别名留门 —— localtest.me / *.nip.io 这类
// 域名会解析回 127.0.0.1（实测），所以域名必须解析后再判一次。
// 完整防护（DNS rebinding、连接时校验）仍应放在网络层。
func isPrivateHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return true // 解析不了就不发请求，别把不确定性带到出站连接里
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return true
		}
	}
	return false
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func inlineBareBase64(ref string) string {
	if strings.HasPrefix(ref, "data:") || strings.HasPrefix(ref, "http://") ||
		strings.HasPrefix(ref, "https://") {
		return ref
	}
	decoded, err := base64.StdEncoding.DecodeString(ref)
	if err != nil || len(decoded) == 0 {
		return ref
	}
	mimeType, err := detectImageMimeType(decoded, "")
	if err != nil {
		return ref
	}
	return "data:" + mimeType + ";base64," + ref
}

// encodeImageForm 把 canonical 的文本字段 + 图片字节写成 multipart。
// 单图用 image，多图用 image[]（OpenAI edits 约定）。
func encodeImageForm(c *imageCanonical, images []imageBlob, mask *imageBlob) ([]byte, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	for key, value := range c.fields {
		switch key {
		case "image", "mask", "response_format":
			continue // 图片单独编码；response_format 是客户端指令，不进 form
		}
		switch typed := value.(type) {
		case string:
			if err := writer.WriteField(key, typed); err != nil {
				return nil, "", err
			}
		case int:
			if err := writer.WriteField(key, fmt.Sprintf("%d", typed)); err != nil {
				return nil, "", err
			}
		default:
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, "", err
			}
			if err := writer.WriteField(key, string(encoded)); err != nil {
				return nil, "", err
			}
		}
	}

	field := "image"
	if len(images) > 1 {
		field = "image[]"
	}
	for index, blob := range images {
		name := fmt.Sprintf("image_%d%s", index+1, extensionForMime(blob.MimeType))
		if err := writeFilePart(writer, field, name, blob); err != nil {
			return nil, "", err
		}
	}
	if mask != nil {
		if err := writeFilePart(writer, "mask", "mask"+extensionForMime(mask.MimeType), *mask); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

func writeFilePart(writer *multipart.Writer, field, filename string, blob imageBlob) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, filename))
	header.Set("Content-Type", blob.MimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(blob.Data)
	return err
}

func extensionForMime(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".bin"
	}
}

func truncateForLog(value string) string {
	if len(value) > 64 {
		return value[:64] + "…"
	}
	return value
}
