package dto

import (
	"fmt"
	"strings"
)

/*
 * apiduo 图像协议配置：渠道级，一份描述「这个上游的图像端点布局」。
 *
 * 为什么不是新增 converter：converter 是「一条入站路由一种协议转换」，表达不了
 * 「同一个 /v1/images/generations 入口，按请求体里有没有 image 走不同上游端点」。
 * 端点布局是上游的事实，应该由脚本按 canonical 决定，见 openai-image-adapter §3.4.1。
 *
 * 配置面只有主端点；从端点由脚本推导，推不出来时才用 EditPath 覆盖。
 */

const (
	ImageUpstreamSeedream  = "seedream"
	ImageUpstreamGPTImage2 = "gpt-image-2"
)

type ImageProtocolConfig struct {
	// Enabled 默认 false。没配置时整条图像协议层完全不介入，行为与上游一字不差。
	Enabled bool `json:"enabled,omitempty"`
	// Upstream: seedream（单端点，image 字段 str|list<str>）/ gpt-image-2（双端点）
	Upstream string `json:"upstream,omitempty"`
	// PrimaryPath 主端点（文生图）。空值默认 /v1/images/generations。
	PrimaryPath string `json:"primary_path,omitempty"`
	// EditPath 从端点覆盖开关。空值由脚本按末段替换推导。
	EditPath string `json:"edit_path,omitempty"`
	// 注意：没有 allow_url_fetch 开关。上游只收 multipart 文件时，
	// http(s) 垫图必然要下载成字节，关掉等于这个功能不可用；
	// 防 SSRF 不是靠「关掉下载」，而是下载层强制校验（协议白名单、
	// 逐跳重定向校验、连接前解析 IP、大小上限、并发上限）。
}

func (c *ImageProtocolConfig) Validate() error {
	if c == nil || !c.Enabled {
		return nil
	}
	switch strings.TrimSpace(c.Upstream) {
	case ImageUpstreamSeedream, ImageUpstreamGPTImage2:
	default:
		return fmt.Errorf(
			"image_protocol.upstream must be %q or %q, got %q",
			ImageUpstreamSeedream,
			ImageUpstreamGPTImage2,
			c.Upstream,
		)
	}
	for name, value := range map[string]string{
		"primary_path": c.PrimaryPath,
		"edit_path":    c.EditPath,
	} {
		trimmed := NormalizePrimaryEndpoint(value)
		if trimmed == "" {
			continue
		}
		// 与 primary_endpoint 同一套形状规则：保存期就拦住「//host」这种
		// 协议相对地址，别让它到请求时才在 URL 构建处炸掉。
		if err := ValidateAbsolutePath(trimmed); err != nil {
			return fmt.Errorf("image_protocol.%s %s: %q", name, err, value)
		}
	}
	return nil
}

// ServesPath 说明图片协议自己接管的入站入口。
//
// 这两个路径的语义在协议层已经定死（折叠成 canonical 再按上游脚本发出），
// 不依赖 advanced_routes 里的转发声明。渠道选择器如果把「没写路由」当成
// 「不支持」，客户端会在协议本来能处理的情况下拿到一个无法解释的 503。
//
// 只放行图片入口：chat 还有纯文本语义，不能替运维决定一条聊天请求要不要变成出图任务。
func (c *ImageProtocolConfig) ServesPath(path string) bool {
	if c == nil || !c.Enabled {
		return false
	}
	switch strings.TrimSuffix(strings.TrimSpace(path), "/") {
	case "/v1/images/generations", "/v1/images/edits":
		return true
	default:
		return false
	}
}

// WithPrimaryFallback 在没显式配 primary_path 时让图片协议跟随渠道主端点。
//
// 只接受「看着像图片端点」的中心（路径里含 /images/）：
// 渠道中心可能是 /v1/chat/completions，把它当图片主端点只会把图发错地方，
// 这种情况保持原来的默认值，比猜更安全。
//
// 返回副本，存储里的 image_protocol 不会被改写。
func (c *ImageProtocolConfig) WithPrimaryFallback(primary string) *ImageProtocolConfig {
	if c == nil || !c.Enabled {
		return c
	}
	if strings.TrimSpace(c.PrimaryPath) != "" {
		return c
	}
	primary = NormalizePrimaryEndpoint(primary)
	if primary == "" || !strings.Contains(primary, "/images/") {
		return c
	}
	next := *c
	next.PrimaryPath = primary
	return &next
}

func (c *ImageProtocolConfig) Primary() string {
	if c == nil || strings.TrimSpace(c.PrimaryPath) == "" {
		return "/v1/images/generations"
	}
	return strings.TrimSpace(c.PrimaryPath)
}
