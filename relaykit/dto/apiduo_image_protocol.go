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
	// 名字描述**协议形状**，不绑型号：方舟换一代模型布局不变，OpenAI
	// 的 dall-e / gpt-image-* 也都是同一套 generations + edits。
	// 存储值就是协议名，不保留历史值（旧配置需迁移，见渠道编辑页）。
	ImageUpstreamARK    = "ark"
	ImageUpstreamOpenAI = "openai"

	// DefaultImagePrimaryPath 渠道没有声明图片主端点时的兜底落点。
	DefaultImagePrimaryPath = "/v1/images/generations"
)

type ImageProtocolConfig struct {
	// Enabled 默认 false。没配置时整条图像协议层完全不介入，行为与上游一字不差。
	Enabled bool `json:"enabled,omitempty"`
	// Upstream: ark（单端点，image 字段 str|list<str>）/ openai（双端点）
	Upstream string `json:"upstream,omitempty"`
	// channelPrimary 是请求期从渠道主端点解析出来的唯一落点，不落库。
	//
	// 以前这里还有一个持久化的 `primary_path`：它和 settings.primary_endpoint
	// 各存一份，谁生效取决于读取顺序，于是出现「界面按 A 显示、请求按 B 发」。
	// 现在只认渠道主端点，这个字段留空即回落到 DefaultImagePrimaryPath。
	channelPrimary string `json:"-"`

	// EditPath 从端点覆盖开关。空值由脚本按末段替换推导。
	EditPath string `json:"edit_path,omitempty"`
	// 注意：没有 allow_url_fetch 开关。上游只收 multipart 文件时，
	// http(s) 垫图必然要下载成字节，关掉等于这个功能不可用；
	// 防 SSRF 不是靠「关掉下载」，而是下载层强制校验（协议白名单、
	// 逐跳重定向校验、连接前解析 IP、大小上限、并发上限）。
}

// NormalizeImageUpstream 把历史型号名收敛成协议名。
//
// 0.0.70 及更早的版本把存储值写成型号（seedream / gpt-image-2），改成协议名
// （ark / openai）时没有随附数据迁移：老渠道 settings 里仍是旧字符串，而校验
// 只认新值 —— 直接发版会让这些渠道的每次图片请求都 400，且报错发生在请求期，
// 运维看不出是配置问题。在读取侧收敛后，老配置不用动、不用等迁移脚本，
// 下次在编辑页保存时自然写成新值。
func NormalizeImageUpstream(raw string) string {
	// 大小写不敏感：有人在 settings JSON 里手填过 ARK，不该因此被拒。
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "seedream", ImageUpstreamARK:
		return ImageUpstreamARK
	case "gpt-image-2", ImageUpstreamOpenAI:
		return ImageUpstreamOpenAI
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func (c *ImageProtocolConfig) Validate() error {
	if c == nil || !c.Enabled {
		return nil
	}
	switch NormalizeImageUpstream(c.Upstream) {
	case ImageUpstreamARK, ImageUpstreamOpenAI:
	default:
		return fmt.Errorf(
			"image_protocol.upstream must be %q or %q, got %q",
			ImageUpstreamARK,
			ImageUpstreamOpenAI,
			c.Upstream,
		)
	}
	for name, value := range map[string]string{
		"edit_path": c.EditPath,
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

// WithChannelPrimary 让渠道主端点成为图片协议的唯一契约。
//
// 主端点是「收敛契约」：上游几个端点都收口到它，所以只要它本身是一个图片路径，
// 就以它为准——即便 image_protocol.primary_path 里还留着历史写入的值。
// 两个字段并存时若各说各话，界面照 primary_endpoint 显示、请求按 primary_path 发，
// 用户改完看不出没生效。
//
// 渠道主端点为空、或不是图片路径（比如 /v1/chat/completions）时退回 primary_path：
// 把聊天路径当图片端点只会把图发错地方，这种情况沿用显式配置，比猜更安全。
//
// 返回副本，存储里的 image_protocol 不会被改写。
func (c *ImageProtocolConfig) WithChannelPrimary(primary string) *ImageProtocolConfig {
	if c == nil || !c.Enabled {
		return c
	}
	center := NormalizePrimaryEndpoint(primary)
	if center == "" || !strings.Contains(center, "/images/") {
		return c
	}
	if c.channelPrimary == center {
		return c
	}
	next := *c
	next.channelPrimary = center
	return &next
}

func (c *ImageProtocolConfig) Primary() string {
	if c == nil || strings.TrimSpace(c.channelPrimary) == "" {
		return DefaultImagePrimaryPath
	}
	return strings.TrimSpace(c.channelPrimary)
}
