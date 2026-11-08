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
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "/") || strings.Contains(trimmed, "?") {
			return fmt.Errorf("image_protocol.%s must be a path starting with /, without query", name)
		}
	}
	return nil
}

func (c *ImageProtocolConfig) Primary() string {
	if c == nil || strings.TrimSpace(c.PrimaryPath) == "" {
		return "/v1/images/generations"
	}
	return strings.TrimSpace(c.PrimaryPath)
}
