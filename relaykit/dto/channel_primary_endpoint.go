package dto

import (
	"fmt"
	"strings"
)

// APIDUO:primary-endpoint:BEGIN
//
// 一个渠道对上游只讲一种「母语」——主端点（primary）。其余入口都是它的投影。
//
// 存储上 primary_endpoint 是渠道级字段；路由的 upstream_path 留空表示「跟随主端点」，
// 由运行时回退补齐。这样换主端点只改一个字段，不必逐条改路由。
//
// 为什么不在保存时把路由都填成绝对路径：配置是结果，意图是中心。把结果铺满
// 每一条路由，下次换主端点又要重写一遍——漂移就是这么来的。
const (
	// primaryEndpointManagementPathPrefix 管理面路径不能当主端点：它们由框架消费，
	// 语义上不是「上游母语」。
	primaryEndpointManagementPathPrefix = "/api/advanced-custom"
)

// NormalizePrimaryEndpoint 去掉空白；空串表示未声明。
func NormalizePrimaryEndpoint(raw string) string {
	return strings.TrimSpace(raw)
}

// ValidatePrimaryEndpoint 只做形状校验：必须是 / 开头的路径，不能带空白、查询串或片段。
func ValidatePrimaryEndpoint(raw string) error {
	path := NormalizePrimaryEndpoint(raw)
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return fmt.Errorf("primary_endpoint must be a path starting with a single /: %q", raw)
	}
	if strings.ContainsAny(path, " \t\r\n") {
		return fmt.Errorf("primary_endpoint must not contain whitespace: %q", raw)
	}
	if strings.ContainsAny(path, "?#") {
		return fmt.Errorf("primary_endpoint must not contain query or fragment: %q", raw)
	}
	if strings.HasPrefix(path, primaryEndpointManagementPathPrefix) {
		return fmt.Errorf("primary_endpoint must not point at a management path: %q", raw)
	}
	return nil
}

// ValidatePrimaryEndpoint 是渠道级入口，nil 安全。
func (s *ChannelOtherSettings) ValidatePrimaryEndpoint() error {
	if s == nil {
		return nil
	}
	return ValidatePrimaryEndpoint(s.PrimaryEndpoint)
}

// WithPrimaryFallback 返回「空 upstream_path 按 primary 补齐」的副本。
//
// 校验（Validate）仍然要求每条路由有上游路径；主端点存在时，空值不再是缺配置，
// 而是「跟随主端点」，所以校验前先补齐，避免把合法配置判成错误。
// 未声明 primary 时原样返回，行为与旧版一致。
func (c *AdvancedCustomConfig) WithPrimaryFallback(primary string) *AdvancedCustomConfig {
	if c == nil {
		return nil
	}
	primary = NormalizePrimaryEndpoint(primary)
	if primary == "" {
		return c
	}
	routes := make([]AdvancedCustomRoute, len(c.Routes))
	copy(routes, c.Routes)
	changed := false
	for i := range routes {
		if strings.TrimSpace(routes[i].UpstreamPath) == "" {
			routes[i].UpstreamPath = primary
			changed = true
		}
	}
	if !changed {
		return c
	}
	next := *c
	next.Routes = routes
	return &next
}

// ResolveRouteUpstream 返回一条路由实际使用的上游路径：
// 路由自己写了就用它（显式覆盖），留空则跟随渠道主端点。
// 两者都为空时返回空串，由调用方决定报什么错。
func (s *ChannelOtherSettings) ResolveRouteUpstream(routeUpstream string) string {
	if value := strings.TrimSpace(routeUpstream); value != "" {
		return value
	}
	if s == nil {
		return ""
	}
	return NormalizePrimaryEndpoint(s.PrimaryEndpoint)
}

// APIDUO:primary-endpoint:END
