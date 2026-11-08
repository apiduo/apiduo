package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// NormalizePrimaryDerivedRoutes 把「上游路径正好等于主端点」的路由收敛成空值。
//
// 空 upstream_path 和「写着 primary 的绝对路径」语义等价（都表示跟随主端点），但只有
// 一种存储形态才不会漂移：换主端点时只改 primary_endpoint，派生路由不必逐条重写。
//
// 用 map 而不是结构体往返：渠道 settings 里可能有本版本不认识的字段，结构体重编码
// 会把它们静默丢掉。只有真的改了才重新编码。
func (channel *Channel) NormalizePrimaryDerivedRoutes() error {
	if channel == nil || strings.TrimSpace(channel.OtherSettings) == "" {
		return nil
	}
	var settings map[string]any
	if err := common.UnmarshalJsonStr(channel.OtherSettings, &settings); err != nil {
		// 解析不了的设置交给后面的 ValidateSettings 报错，这里不抢先下结论。
		return nil
	}
	changed := false
	// 主端点本身也收敛：去掉首尾空白，空串就删键。否则存储里会留下
	// " /v1/xxx " 这种读得懂但写不对的形态，页面回显也会带着空格。
	rawPrimary := settingString(settings["primary_endpoint"])
	primary := dto.NormalizePrimaryEndpoint(rawPrimary)
	if primary != rawPrimary {
		if primary == "" {
			delete(settings, "primary_endpoint")
		} else {
			settings["primary_endpoint"] = primary
		}
		changed = true
	}
	if primary != "" {
		if custom, ok := settings["advanced_custom"].(map[string]any); ok {
			if routes, ok := custom["advanced_routes"].([]any); ok {
				for _, item := range routes {
					route, ok := item.(map[string]any)
					if !ok {
						continue
					}
					if strings.TrimSpace(settingString(route["upstream_path"])) == primary {
						route["upstream_path"] = ""
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return nil
	}
	encoded, err := common.Marshal(settings)
	if err != nil {
		return err
	}
	channel.OtherSettings = string(encoded)
	return nil
}

func settingString(value any) string {
	text, _ := value.(string)
	return text
}
