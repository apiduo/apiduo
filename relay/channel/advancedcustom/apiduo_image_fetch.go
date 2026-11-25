package advancedcustom

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

/*
 * URL 垫图下载层：把 http(s) / data URI / 裸 base64 统一成字节。
 *
 * 这里的每一次出站都受三重约束：SSRF（解析后校验、逐跳重定向再校验）、
 * 时间（首字节 / 单张 / 整批三级预算）、体积（单张上限）。
 * 协议转换与 multipart 编码在 apiduo_image_script.go，那侧不碰网络。
 */

const (
	// imageURLHeaderTimeout 是「这个目标还能不能用」的判据：连上之后迟迟不给
	// 响应头，说明链路不通，不必再等 body。
	imageURLHeaderTimeout = 10 * time.Second
	// imageURLFetchTimeout 是单张图端到端兜底（含读完 body），不是首字节判据。
	// 10s 只够「678KB、单张、直连」；同一时刻最多 4 张在传，其余排队共享
	// 出口带宽，跨境到国内 CDN 时一张 2MB 的图就要十几秒 —— 把 10s 当总预算
	// 会把「慢但能下」误判成失败（2026-11-25 core-1 线上 6 张并发全部 400，
	// 而同时段同 URL 单张实测仅 1.4~2.4s）。
	imageURLFetchTimeout = 60 * time.Second
	// imageRefBatchTimeout 是整批垫图的总预算。单张 60s 且最多 4 路在传，墙钟
	// 时间会随排队叠加；没有这一层，6 张图最坏要等 120s 才失败。
	imageRefBatchTimeout = 90 * time.Second
	// imageURLDialTimeout 是单次 TCP 连接与 TLS 握手的预算。5s 对跨境偏紧：
	// 7 天里 3 次 TLS handshake timeout 全落在 5s 边界上。
	imageURLDialTimeout = 10 * time.Second
	// imageURLDialBudget 是一次解析结果内遍历多个 CDN 节点的总预算，避免节点
	// 全黑洞时白等 N × dial。
	imageURLDialBudget       = 20 * time.Second
	imageURLFetchMaxRedirect = 3
	// imageRefDownloadWorkers 是单请求内垫图下载的并发上限。图生图常见 4~8 张，
	// 8 路能把绝大多数请求压在一轮里，不再有「第二轮排队」这段额外墙钟；目标
	// 都是 CDN（七牛 / 火山 TOS），单 IP 8 并发远低于其限流阈值。真超过 8 张时
	// 仍是滚动补位，不会空等整轮。
	imageRefDownloadWorkers = 8
	// imageRefFetchAttempts 是单张图的最大尝试次数（含首次）。失败代价是整个
	// 生成请求作废，而一次重试只要几百毫秒，所以给到 3 次。
	imageRefFetchAttempts = 3

	imageRefUserAgent = "apiduo-image-fetcher/1.0"
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

// imageRefHTTPClient 是正常下载复用的连接池（每次 new client 会让 N 张垫图做
// N 次 TLS 握手，慢目标下直接把请求拖死）；imageRefRetryClient 只在重试时用，
// 且不复用连接 —— 坏连接可能还挂在池里，直接重试会二次撞上同一条。
var (
	imageRefHTTPClient  = newImageRefClient(false)
	imageRefRetryClient = newImageRefClient(true)
)

func newImageRefClient(freshConnection bool) *http.Client {
	return &http.Client{
		Timeout:   imageURLFetchTimeout,
		Transport: newImageRefTransport(freshConnection),
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			// Go 默认最多跟 10 跳且不重验 host：一个公网 URL 302 到内网就绕过 SSRF 防线。
			if len(via) >= imageURLFetchMaxRedirect {
				return fmt.Errorf("image URL redirects more than %d times", imageURLFetchMaxRedirect)
			}
			return validateImageRefURL(request.Context(), request.URL)
		},
	}
}

func newImageRefTransport(freshConnection bool) *http.Transport {
	return &http.Transport{
		// 不读环境代理：SSRF 防线建立在「本进程解析、直连校验过的 IP」上，走代理
		// 等于把目标交给代理，校验形同虚设；而且 127.0.0.1 的本地代理会被
		// imageRefAllowAddress 直接判成内网，整条下载路径 100% 失败。
		Proxy:                 nil,
		DialContext:           dialImageRef,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       10 * time.Second,
		TLSHandshakeTimeout:   imageURLDialTimeout,
		ResponseHeaderTimeout: imageURLHeaderTimeout,
		// 图片已经压过一遍，再协商 gzip 只是多一轮头解析。
		DisableCompression: true,
		DisableKeepAlives:  freshConnection,
		// 刻意不开 h2：h2 对同一 host 只维持一条连接，跨境丢包后这条连接会变成
		// 黑洞（TCP 仍是 ESTABLISHED，Go 的 h2 不做 PING 探测、也不重建），4 个
		// worker 会一起卡到超时。2026-11-25 线上实证：6 条并发失败共用源端口
		// 49686，而同时段新建连接 20/20 成功。静态图片走 h1 多连接更稳。
		ForceAttemptHTTP2: false,
	}
}

// imageRefDialCursor 让同一域名的多个 CDN 节点被轮流使用：总从第一条开始的话，
// 那条节点一旦对这个出口不可用，每次请求都要先等它超时。
var imageRefDialCursor atomic.Uint32

// dialImageRef 先解析再连，校验与连接用同一批地址，避免 DNS rebinding 在两次
// 解析之间换地址。IPv4 优先 —— 容器里的 IPv6 出口经常是「有地址没路由」的黑洞。
func dialImageRef(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	resolved, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	v4 := make([]net.IP, 0, len(resolved))
	v6 := make([]net.IP, 0, len(resolved))
	for _, entry := range resolved {
		if err := imageRefAllowAddress(entry.IP); err != nil {
			return nil, err
		}
		if entry.IP.To4() != nil {
			v4 = append(v4, entry.IP)
			continue
		}
		v6 = append(v6, entry.IP)
	}

	// 节点全黑洞时不要逐个等满 dial 超时。
	ctx, cancel := context.WithTimeout(ctx, imageURLDialBudget)
	defer cancel()

	dialer := &net.Dialer{Timeout: imageURLDialTimeout, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, family := range [][]net.IP{v4, v6} {
		for _, ip := range rotateImageRefIPs(family) {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return nil, lastErr
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable address for %q", host)
	}
	return nil, lastErr
}

func rotateImageRefIPs(ips []net.IP) []net.IP {
	if len(ips) < 2 {
		return ips
	}
	offset := int(imageRefDialCursor.Add(1)) % len(ips)
	rotated := make([]net.IP, 0, len(ips))
	rotated = append(rotated, ips[offset:]...)
	rotated = append(rotated, ips[:offset]...)
	return rotated
}

type imageReferenceFetchError struct {
	err error
	// upstreamStatus 只在目标回了非 2xx 时非零：5xx / 408 / 429 值得服务端自己
	// 再试一次，其它 4xx 换多少次都一样。
	upstreamStatus int
}

func (e *imageReferenceFetchError) Error() string { return e.err.Error() }
func (e *imageReferenceFetchError) Unwrap() error { return e.err }

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

// fetchImageRef 下载一张垫图，最多尝试 imageRefFetchAttempts 次。跨境链路的
// 失败是偶发的（TLS 握手超时、连接被 RST、等不到响应头）：让整个生成请求为
// 一次抖动作废不划算，重试通常就过（2026-11-25 线上实证）。状态码与内容问题
// 不重试，避免把配置错误放大成重试风暴。
func fetchImageRef(ctx context.Context, ref string) (imageBlob, error) {
	var lastErr error
	attempts := 0
	for attempt := range imageRefFetchAttempts {
		if attempt > 0 {
			if err := waitImageRefRetry(ctx, attempt); err != nil {
				break
			}
		}
		attempts++
		// 重试必须换一条新连接：坏连接可能还挂在池子里。
		client := imageRefHTTPClient
		if attempt > 0 {
			client = imageRefRetryClient
		}
		blob, err := downloadImageRef(ctx, client, ref)
		if err == nil {
			if attempts > 1 {
				common.SysLog(fmt.Sprintf("image reference download recovered after %d attempts: host=%s",
					attempts, imageRefLogHost(ref)))
			}
			return blob, nil
		}
		lastErr = err
		if ctx.Err() != nil || !isTransientImageFetchError(err) {
			break
		}
	}
	if lastErr == nil {
		return imageBlob{}, ctx.Err()
	}
	// 同批里另一张失败导致的取消不是这个目标的错，别打成下载失败；
	// 整批预算到期（DeadlineExceeded）仍要留痕，那是真的没下完。
	if errors.Is(ctx.Err(), context.Canceled) {
		return imageBlob{}, lastErr
	}
	if attempts > 1 {
		lastErr = fmt.Errorf("%w (after %d attempts)", lastErr, attempts)
	}
	// 这类失败不进请求日志，线上只能靠这条 syslog 回溯目标与尝试次数。
	common.SysError(fmt.Sprintf("image reference download failed: host=%s attempts=%d err=%v",
		imageRefLogHost(ref), attempts, lastErr))
	return imageBlob{}, lastErr
}

// imageRefLogHost 只取域名：URL 可能带签名参数，整条落日志等于泄密。
func imageRefLogHost(ref string) string {
	if parsed, err := url.Parse(ref); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return truncateForLog(ref)
}

// waitImageRefRetry 是两次尝试之间的短退避：跨境抖动窗口通常只有几百毫秒，
// 立刻重试容易撞在同一段拥塞上。
func waitImageRefRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt) * 250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func downloadImageRef(ctx context.Context, client *http.Client, ref string) (imageBlob, error) {
	parsed, err := url.Parse(ref)
	if err != nil {
		return imageBlob{}, fmt.Errorf("invalid image URL %q", truncateForLog(ref))
	}
	if err := validateImageRefURL(ctx, parsed); err != nil {
		return imageBlob{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
	if err != nil {
		return imageBlob{}, fmt.Errorf("invalid image URL %q", truncateForLog(ref))
	}
	request.Header.Set("Accept", "image/*")
	request.Header.Set("User-Agent", imageRefUserAgent)
	resp, err := client.Do(request)
	if err != nil {
		return imageBlob{}, &imageReferenceFetchError{err: fmt.Errorf("download image: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("download image: upstream returned %d", resp.StatusCode)
		// 408/429 与 5xx 是「对方现在给不了」，算网关侧；其余 4xx 是这个 URL
		// 本身不行，客户端换一个就能过。
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusTooManyRequests {
			return imageBlob{}, &imageReferenceFetchError{err: err, upstreamStatus: resp.StatusCode}
		}
		return imageBlob{}, err
	}
	// 有 Content-Length 就先判大小，别把 100MB 读完才发现超限。
	if resp.ContentLength > int64(imageMaxRefBytes) {
		return imageBlob{}, fmt.Errorf("image exceeds %d bytes", imageMaxRefBytes)
	}
	limit := int64(imageMaxRefBytes) + 1
	var data []byte
	if size := resp.ContentLength; size > 0 && size <= limit {
		// 已知长度就一次分配到位：io.ReadAll 与 bytes.Buffer 都会翻倍扩容，
		// 让 8 张几 MB 的图多占一份等量内存（实测 16MB payload 峰值 36MB）。
		// http body 本身在 Content-Length 处 EOF，ReadFull 不会漏读也不会多读。
		data = make([]byte, size)
		if _, err := io.ReadFull(resp.Body, data); err != nil {
			return imageBlob{}, &imageReferenceFetchError{err: fmt.Errorf("read image: %w", err)}
		}
	} else {
		read, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		if err != nil {
			return imageBlob{}, &imageReferenceFetchError{err: fmt.Errorf("read image: %w", err)}
		}
		data = read
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
	// 单张 60s、最多 4 路在传，其余排队；不设整批上限时墙钟会随排队叠加。
	// 调用方给更短的 ctx 时，WithTimeout 取更早的那个 deadline。
	ctx, cancel := context.WithTimeout(ctx, imageRefBatchTimeout)
	defer cancel()
	if len(refs) == 1 {
		blob, err := resolveImageRef(ctx, refs[0])
		if err != nil {
			return nil, fmt.Errorf("image reference: %w", err)
		}
		return []imageBlob{blob}, nil
	}

	// 命名与整批预算的 ctx 分开：同一个作用域里不能重复 := 声明。
	groupCtx, groupCancel := context.WithCancel(ctx)
	defer groupCancel()

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
			case <-groupCtx.Done():
				return
			}
			defer func() { <-sem }()

			blob, err := resolveImageRef(groupCtx, ref)
			if err != nil {
				once.Do(func() {
					firstErr = fmt.Errorf("image reference: %w", err)
					groupCancel()
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
func validateImageRefURL(ctx context.Context, target *url.URL) error {
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
	// 用带 ctx 的解析：无 ctx 的 net.LookupIP 会绕开整批预算，DNS 抖动时
	// 请求在「还没开始下载」的阶段就能挂满两个解析超时。
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("image reference host %q cannot be resolved: %w", host, err)
	}
	for _, addr := range addrs {
		if err := imageRefAllowAddress(addr.IP); err != nil {
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

// isTransientImageFetchError 区分「链路抖了一下」和「目标明确拒绝」。
func isTransientImageFetchError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// 5xx / 408 / 429：服务端自己重试比让客户端重试更快，也更省一次往返。
	var fetchErr *imageReferenceFetchError
	if errors.As(err, &fetchErr) && fetchErr.upstreamStatus != 0 {
		return true
	}
	msg := err.Error()
	for _, needle := range []string{
		"TLS handshake timeout",
		"connection reset by peer",
		"unexpected EOF",
		"http2: timeout awaiting response headers",
		"GOAWAY",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func truncateForLog(value string) string {
	if len(value) > 64 {
		return value[:64] + "…"
	}
	return value
}
