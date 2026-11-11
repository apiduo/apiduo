package constant

import "testing"

// Playground 的图片入口不在 /v1 前缀下。模式一旦回落成 RelayModeUnknown，
// 校验层就会走「把表单值原样转成 JSON」的默认分支：multipart 里的 n 恒为
// 字符串，写进 *uint 会直接报
// `json: cannot unmarshal string into Go struct field Alias.n of type uint`。
// 这条用例锁住两个 /pg 入口的真实模式。
func TestPath2RelayModePlaygroundImagePaths(t *testing.T) {
	cases := []struct {
		path string
		want int
	}{
		{"/pg/images/edits", RelayModeImagesEdits},
		{"/pg/images/generations", RelayModeImagesGenerations},
		// 编辑入口带查询串时也要命中。
		{"/pg/images/edits?group=default", RelayModeImagesEdits},
		// 上游的 /v1 入口不能因为上面的提前返回而失效。
		{"/v1/images/edits", RelayModeImagesEdits},
		{"/v1/images/generations", RelayModeImagesGenerations},
		// playground 的文本入口此前已有映射，回归保护。
		{"/pg/chat/completions", RelayModeChatCompletions},
		// 未声明的 /pg 路径保持未知，避免误判成图片请求。
		{"/pg/unknown", RelayModeUnknown},
	}
	for _, tc := range cases {
		if got := Path2RelayMode(tc.path); got != tc.want {
			t.Fatalf("Path2RelayMode(%q) = %d, want %d", tc.path, got, tc.want)
		}
	}
}
