package advancedcustom

import (
	"bytes"
	"context"
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
	"sync"
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
 * 不放进纯函数折叠层。上游只收 multipart 文件时，URL 垫图必然要下载；
 * 防 SSRF 靠下载层的强制校验，而不是「关掉下载」。
 */

const (
	imageURLFetchTimeout     = 10 * time.Second
	imageURLDialTimeout      = 5 * time.Second
	imageURLFetchMaxRedirect = 3
	// imageRefDownloadWorkers 是单请求内垫图下载的并发上限。太高会打爆目标站点，
	// 太低会让 10 张图串行到请求超时。
	imageRefDownloadWorkers = 4
)

// imageMaxRefBytes 是单张引用（data URI / 下载 / 裸 base64）的大小上限。
// 用变量而非常量：单测要把它临时调小，避免造 20MB 夹具。
var imageMaxRefBytes = 20 << 20

// imageRefAllowAddress 是地址准入钩子。默认拒绝回环/私网/链路本地/多播/保留段；
// 单测替换它把 httptest 的 127.0.0.1 放进白名单，生产路径不替换。
var imageRefAllowAddress = func(ip net.IP) error {
	if isPrivateIP(ip) {
		return fmt.Errorf("image reference resolves to a non-public address %s", ip)
	}
	return nil
}

// imageRefBlockedCIDRs 补 IsPrivate() 覆盖不到的段：CGNAT、保留/文档/基准测试段。
var imageRefBlockedCIDRs = func() []*net.IPNet {
	blocks := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"100::/64", "2001:db8::/32",
	}
	out := make([]*net.IPNet, 0, len(blocks))
	for _, block := range blocks {
		if _, parsed, err := net.ParseCIDR(block); err == nil {
			out = append(out, parsed)
		}
	}
	return out
}()

// imageRefHTTPClient 在所有下载之间复用：连接池 + 逐跳校验 + 连接前校验 IP。
// 每次 new client 会让 N 张垫图做 N 次 TLS 握手，慢目标下直接把请求拖死。
var imageRefHTTPClient = &http.Client{
	Timeout: imageURLFetchTimeout,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   imageURLDialTimeout,
		ResponseHeaderTimeout: imageURLFetchTimeout,
		ForceAttemptHTTP2:     true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("resolve %q: %w", host, err)
			}
			dialer := &net.Dialer{Timeout: imageURLDialTimeout, KeepAlive: 30 * time.Second}
			var lastErr error
			for _, ip := range ips {
				// 校验与连接用同一批 IP：先解析再连，避免 rebinding 在两次解析之间换地址。
				if err := imageRefAllowAddress(ip); err != nil {
					return nil, err
				}
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("no usable address for %q", host)
			}
			return nil, lastErr
		},
	},
	CheckRedirect: func(request *http.Request, via []*http.Request) error {
		// Go 默认最多跟 10 跳且不重验 host：一个公网 URL 302 到内网就绕过 SSRF 防线。
		if len(via) >= imageURLFetchMaxRedirect {
			return fmt.Errorf("image URL redirects more than %d times", imageURLFetchMaxRedirect)
		}
		return validateImageRefURL(request.URL)
	},
}

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

	ctx := context.Background()
	images, err := resolveImageRefs(ctx, c.imageRefs())
	if err != nil {
		return nil, err
	}

	var mask *imageBlob
	if raw, exists := c.fields["mask"]; exists {
		if ref, ok := raw.(string); ok {
			blob, err := resolveImageRef(ctx, ref)
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

// resolveImageRef 把三种引用统一成字节。data URI 与裸 base64 是本地解码；
// http(s) 一律下载，SSRF 防护在 validateImageRefURL 与连接层强制。
func resolveImageRef(ctx context.Context, ref string) (imageBlob, error) {
	ref = strings.TrimSpace(ref)

	if strings.HasPrefix(ref, "data:") {
		return decodeDataURI(ref)
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return fetchImageRef(ctx, ref)
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

func fetchImageRef(ctx context.Context, ref string) (imageBlob, error) {
	parsed, err := url.Parse(ref)
	if err != nil {
		return imageBlob{}, fmt.Errorf("invalid image URL %q", truncateForLog(ref))
	}
	if err := validateImageRefURL(parsed); err != nil {
		return imageBlob{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
	if err != nil {
		return imageBlob{}, fmt.Errorf("invalid image URL %q", truncateForLog(ref))
	}
	request.Header.Set("Accept", "image/*")
	resp, err := imageRefHTTPClient.Do(request)
	if err != nil {
		return imageBlob{}, fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return imageBlob{}, fmt.Errorf("download image: upstream returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(imageMaxRefBytes)+1))
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

// resolveImageRefs 并发下载多张垫图（上限 imageRefDownloadWorkers）并保持客户端顺序；
// 任一失败取消其余，避免为一个必然失败的请求继续下载剩下的图。
func resolveImageRefs(ctx context.Context, refs []string) ([]imageBlob, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) == 1 {
		blob, err := resolveImageRef(ctx, refs[0])
		if err != nil {
			return nil, fmt.Errorf("image reference: %w", err)
		}
		return []imageBlob{blob}, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	blobs := make([]imageBlob, len(refs))
	sem := make(chan struct{}, imageRefDownloadWorkers)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error

	for index, ref := range refs {
		index, ref := index, ref
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			blob, err := resolveImageRef(ctx, ref)
			if err != nil {
				once.Do(func() {
					firstErr = fmt.Errorf("image reference: %w", err)
					cancel()
				})
				return
			}
			blobs[index] = blob
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return blobs, nil
}

// validateImageRefURL 是 SSRF 第一道闸：只允许 http(s)，host 必须可解析且不含内网地址。
// 重定向每一跳都会再走一次（见 imageRefHTTPClient.CheckRedirect）。
func validateImageRefURL(target *url.URL) error {
	if target == nil {
		return errors.New("image reference URL is nil")
	}
	switch strings.ToLower(target.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("image reference scheme %q is not allowed", target.Scheme)
	}
	host := strings.TrimSpace(target.Hostname())
	if host == "" {
		return errors.New("image reference URL has no host")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("image reference host %q is not allowed", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		return imageRefAllowAddress(ip)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("image reference host %q cannot be resolved: %w", host, err)
	}
	for _, ip := range ips {
		if err := imageRefAllowAddress(ip); err != nil {
			return err
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	for _, block := range imageRefBlockedCIDRs {
		if block.Contains(ip) {
			return true
		}
	}
	return false
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
		case "image", "mask":
			continue // 图片单独编码成文件 part
			// response_format 决定上游返回 url 还是 b64_json，必须随 form 下传。
			// /v1/images/edits 同样认这个字段（bfl2openai 的 Form(None)），
			// 丢掉会让客户端点名要 url 时仍收到默认的大 b64_json。
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
