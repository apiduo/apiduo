package dto

import (
	"errors"
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

// NormalizePrimaryEndpoint 去掉空白与末尾斜杠；空串表示未声明。
//
// 末尾斜杠不是语义的一部分：留着会让「/x」和「/x/」被当成两条不同的上游，
// 保存时的「等于主端点就收敛成空」也按字符串相等判断，两边写法不同就收敛不掉。
// 只裁真正有内容的路径，免得把「//」（非法）裁成空、把非法输入当成未声明。
func NormalizePrimaryEndpoint(raw string) string {
	path := strings.TrimSpace(raw)
	if trimmed := strings.TrimRight(path, "/"); trimmed != "" {
		return trimmed
	}
	return path
}

// ValidatePrimaryEndpoint 只做形状校验：必须是 / 开头的路径，不能带空白、查询串或片段。
func ValidatePrimaryEndpoint(raw string) error {
	path := NormalizePrimaryEndpoint(raw)
	if path == "" {
		return nil
	}
	if err := ValidateAbsolutePath(path); err != nil {
		return fmt.Errorf("primary_endpoint %s: %q", err, raw)
	}
	return nil
}

// ValidateAbsolutePath 校验「/ 开头的上游路径」的公共形状：单斜杠开头、无空白、
// 无查询串或片段、不指向管理面。
//
// primary_endpoint 和 image_protocol 的 primary_path / edit_path 指向同一个地方，
// 形状规则必须一致：只校验前者会让协议里那两条在保存时放行、在请求时才失败，
// 而且报错发生在每次请求上，运维看不出是配置问题。错误文案只描述问题本身，
// 由调用方补字段名。
func ValidateAbsolutePath(path string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return errors.New("must be a path starting with a single /")
	}
	if strings.ContainsAny(path, " \t\r\n") {
		return errors.New("must not contain whitespace")
	}
	if strings.ContainsAny(path, "?#") {
		return errors.New("must not contain query or fragment")
	}
	if strings.HasPrefix(path, primaryEndpointManagementPathPrefix) {
		return errors.New("must not point at a management path")
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
