package model

import (
	"encoding/json"
	"strings"
)

// apiduoResultURLFromData 从任务原始回包里找成片地址。
//
// 轮询器写 PrivateData.ResultURL 依赖 adaptor 的 result_url_path 命中；上游把
// 地址放在配置没覆盖到的位置时，任务照样是 SUCCESS，但前端拿不到地址 ——
// 卡片只显示「已成功」，没有播放器，用户以为没做成。
//
// 这里按几个常见形状兜底读一次（只读，不改 Data）：
//
//	{"content": {"video_url": "…"}}        方舟任务对象（hailuo / seedance）
//	{"video_url": "…"} / {"url": "…"}      各家自建形状
//	{"data": [{"url": "…"}]}               OpenAI 异步图像形状
//
// 只认 http(s) 与 data: 两种前缀：上游失败时回的是空串或错误文案，把
// "upstream reported failure" 当播放地址会让卡片渲染一个坏掉的 <video>。
func apiduoResultURLFromData(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return ""
	}
	candidates := []any{
		root["video_url"],
		root["url"],
		root["result_url"],
		root["output_url"],
	}
	if content, ok := root["content"].(map[string]any); ok {
		candidates = append(candidates, content["video_url"], content["url"])
	}
	if list, ok := root["data"].([]any); ok && len(list) > 0 {
		if first, ok := list[0].(map[string]any); ok {
			candidates = append(candidates, first["url"], first["video_url"])
		}
	}
	for _, candidate := range candidates {
		value, ok := candidate.(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "data:") {
			return value
		}
	}
	return ""
}
