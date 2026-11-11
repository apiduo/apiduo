package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

// 别名路由靠这个判定决定「这条方舟形状的提交归渠道还是归插件」：
// 判错就会把请求送到插件（客户端收到不相干的 404），或者把插件该管的模型抢过来。
func TestApiduoAdvancedAsyncServes(t *testing.T) {
	previous := channelsIDM
	t.Cleanup(func() { channelsIDM = previous })

	asyncSettings := `{"advanced_async":{"submit":{"upstream_path":"/v1/tasks"},"query":{"upstream_path":"/v1/tasks/{task_id}"}}}`
	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "seedance-x, Seedream-Y", OtherSettings: asyncSettings},
		2: {Id: 2, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusManuallyDisabled,
			Models: "disabled-model", OtherSettings: asyncSettings},
		3: {Id: 3, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "unconfigured-model"}, // 没有 advanced_async：这条渠道不该被当成可用
		4: {Id: 4, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled,
			Models: "custom-model", OtherSettings: asyncSettings},
	}

	cases := []struct {
		name  string
		model string
		want  bool
	}{
		{"声明过的模型", "seedance-x", true},
		{"模型名大小写不敏感", "seedream-y", true},
		{"前后空白不影响", "  seedance-x  ", true},
		{"未声明的模型", "not-listed", false},
		{"禁用渠道不算", "disabled-model", false},
		{"没配 advanced_async 的渠道不算", "unconfigured-model", false},
		{"别的渠道类型不算", "custom-model", false},
		{"空模型名", "", false},
	}

	for _, tc := range cases {
		if got := ApiduoAdvancedAsyncServes(tc.model); got != tc.want {
			t.Fatalf("%s: ApiduoAdvancedAsyncServes(%q) = %v, want %v", tc.name, tc.model, got, tc.want)
		}
	}

	// "*" 是这类渠道的「任意模型」写法：它必须接住任何名字，否则方舟形状的
	// 提交会在没有任何通配渠道时落到插件上。
	channelsIDM = map[int]*Channel{
		5: {Id: 5, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "*", OtherSettings: asyncSettings},
	}
	if !ApiduoAdvancedAsyncServes("whatever-1") {
		t.Fatal("wildcard channel must claim any model")
	}
}

// 入站端点由渠道自己声明，dispatcher 只接管「有渠道认领且该渠道声明了这个
// 模型」的路径。协议必须一起返回：它决定客户端拿到的是标准 Images 响应
// 还是任务对象，猜错就是客户端读不懂的 JSON。
func TestApiduoAsyncIncomingServes(t *testing.T) {
	previous := channelsIDM
	t.Cleanup(func() { channelsIDM = previous })

	imagesConfig := `{"advanced_async":{"protocol":"images","incoming_paths":["/v1/images/generations/async"],"submit":{"upstream_path":"/v1/images/generations"},"query":{"upstream_path":"/v1/images/generations/{task_id}"}}}`
	arkConfig := `{"advanced_async":{"protocol":"ark","incoming_paths":["/v1/video/generations"],"submit":{"upstream_path":"/api/v3/contents/generations/tasks"},"query":{"upstream_path":"/api/v3/contents/generations/tasks/{task_id}"}}}`
	legacyConfig := `{"advanced_async":{"incoming_paths":["/open/legacy"],"submit":{"upstream_path":"/v1/tasks"},"query":{"upstream_path":"/v1/tasks/{task_id}"}}}`

	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "gpt-image-1", OtherSettings: imagesConfig},
		2: {Id: 2, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "seedance-1", OtherSettings: arkConfig},
		3: {Id: 3, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "*", OtherSettings: legacyConfig},
		4: {Id: 4, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusManuallyDisabled,
			Models: "disabled-model", OtherSettings: imagesConfig},
		5: {Id: 5, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusEnabled,
			Models: "sync-model", OtherSettings: imagesConfig},
	}

	cases := []struct {
		name     string
		path     string
		model    string
		protocol string
		want     bool
	}{
		{"图像入口给出 images", "/v1/images/generations/async", "gpt-image-1", constant.ApiduoAsyncProtocolImages, true},
		{"图像入口大小写不敏感", "/v1/images/generations/async", "GPT-Image-1", constant.ApiduoAsyncProtocolImages, true},
		{"方舟入口给出 ark", "/v1/video/generations", "seedance-1", constant.ApiduoAsyncProtocolArk, true},
		{"没写协议按自定义", "/open/legacy", "whatever", constant.ApiduoAsyncProtocolCustom, true},
		{"通配渠道接任意模型", "/open/legacy", "another", constant.ApiduoAsyncProtocolCustom, true},
		{"没声明的路径不算", "/v1/images/generations", "gpt-image-1", "", false},
		{"没声明的模型不算", "/v1/images/generations/async", "dall-e-3", "", false},
		{"禁用渠道不算", "/v1/images/generations/async", "disabled-model", "", false},
		{"别的渠道类型不算", "/v1/images/generations/async", "sync-model", "", false},
		{"空模型不算", "/v1/images/generations/async", "", "", false},
		{"空路径不算", "", "gpt-image-1", "", false},
	}

	for _, tc := range cases {
		protocol, ok := ApiduoAsyncIncomingServes(tc.path, tc.model)
		if ok != tc.want || protocol != tc.protocol {
			t.Fatalf("%s: ApiduoAsyncIncomingServes(%q, %q) = (%q, %v), want (%q, %v)",
				tc.name, tc.path, tc.model, protocol, ok, tc.protocol, tc.want)
		}
	}
}

// 同一路径被两条渠道声明且协议不一致时取 images：它的响应形状最窄，
// 猜成 ark 只会把任务对象塞给期待 Images 响应的客户端。
func TestApiduoAsyncIncomingServesPrefersImages(t *testing.T) {
	previous := channelsIDM
	t.Cleanup(func() { channelsIDM = previous })

	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "shared-model", OtherSettings: `{"advanced_async":{"protocol":"ark","incoming_paths":["/shared/tasks"],"submit":{"upstream_path":"/v1/tasks"},"query":{"upstream_path":"/v1/tasks/{task_id}"}}}`},
		2: {Id: 2, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "shared-model", OtherSettings: `{"advanced_async":{"protocol":"images","incoming_paths":["/shared/tasks"],"submit":{"upstream_path":"/v1/images"},"query":{"upstream_path":"/v1/images/{task_id}"}}}`},
	}
	if protocol, ok := ApiduoAsyncIncomingServes("/shared/tasks", "shared-model"); !ok || protocol != constant.ApiduoAsyncProtocolImages {
		t.Fatalf("got (%q, %v), want images", protocol, ok)
	}
}

// 轮询那一半在拿到任务号之前就要决定响应形状，只能看路径。
func TestApiduoAsyncIncomingProtocol(t *testing.T) {
	previous := channelsIDM
	t.Cleanup(func() { channelsIDM = previous })

	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "gpt-image-1", OtherSettings: `{"advanced_async":{"protocol":"images","incoming_paths":["/v1/images/generations/async"],"submit":{"upstream_path":"/v1/images"},"query":{"upstream_path":"/v1/images/{task_id}"}}}`},
	}

	if protocol, ok := ApiduoAsyncIncomingProtocol("/v1/images/generations/async"); !ok || protocol != constant.ApiduoAsyncProtocolImages {
		t.Fatalf("got (%q, %v), want images", protocol, ok)
	}
	if _, ok := ApiduoAsyncIncomingProtocol("/v1/images/tasks"); ok {
		t.Fatal("unclaimed path must not resolve to a protocol")
	}
}

// 图像异步按业务分段是常用写法：即梦、海螺各占一段，客户端按业务决定打
// 哪条入口。模型与路径必须同时匹配，否则同一个模型会从别人的业务入口进来。
func TestApiduoAsyncIncomingServesBizScopedImages(t *testing.T) {
	previous := channelsIDM
	t.Cleanup(func() { channelsIDM = previous })

	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "jimeng-3.0", OtherSettings: `{"advanced_async":{"protocol":"images","incoming_paths":["/jimeng/v1/images/generations"],"submit":{"upstream_path":"/v1/images/generations"},"query":{"upstream_path":"/v1/images/generations/{task_id}"}}}`},
		2: {Id: 2, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "hailuo-2.3", OtherSettings: `{"advanced_async":{"protocol":"images","incoming_paths":["/hailuo/v1/images/generations"],"submit":{"upstream_path":"/v1/images/generations"},"query":{"upstream_path":"/v1/images/generations/{task_id}"}}}`},
	}

	cases := []struct {
		name  string
		path  string
		model string
		want  bool
	}{
		{"即梦业务入口", "/jimeng/v1/images/generations", "jimeng-3.0", true},
		{"海螺业务入口", "/hailuo/v1/images/generations", "hailuo-2.3", true},
		{"别的业务入口不算", "/hailuo/v1/images/generations", "jimeng-3.0", false},
		{"同步入口仍然不可用", "/v1/images/generations", "jimeng-3.0", false},
	}
	for _, tc := range cases {
		protocol, ok := ApiduoAsyncIncomingServes(tc.path, tc.model)
		if ok != tc.want {
			t.Fatalf("%s: ApiduoAsyncIncomingServes(%q, %q) = (%q, %v), want %v",
				tc.name, tc.path, tc.model, protocol, ok, tc.want)
		}
		if ok && protocol != constant.ApiduoAsyncProtocolImages {
			t.Fatalf("%s: protocol = %q, want images", tc.name, protocol)
		}
	}
}

// 提交链上只剩模型名（入口路径已被重写），协议判定只能按模型走。
// 多条渠道声明同一模型时 ark 优先：方舟形状最窄，拿 Images 响应去填
// 只会得到客户端读不懂的 JSON。
func TestApiduoAsyncModelProtocolPrefersArk(t *testing.T) {
	previous := channelsIDM
	t.Cleanup(func() { channelsIDM = previous })

	channelsIDM = map[int]*Channel{
		1: {Id: 1, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "shared-model", OtherSettings: `{"advanced_async":{"protocol":"images","incoming_paths":["/v1/images/generations/async"],"submit":{"upstream_path":"/v1/images/generations"},"query":{"upstream_path":"/v1/images/generations/{task_id}"}}}`},
		2: {Id: 2, Type: constant.ChannelTypeAdvancedAsync, Status: common.ChannelStatusEnabled,
			Models: "shared-model", OtherSettings: `{"advanced_async":{"protocol":"ark","incoming_paths":["/api/v3/contents/generations/tasks"],"submit":{"upstream_path":"/api/v3/contents/generations/tasks"},"query":{"upstream_path":"/api/v3/contents/generations/tasks/{task_id}"}}}`},
	}

	protocol, claimed := ApiduoAsyncModelProtocol("shared-model")
	if !claimed || protocol != constant.ApiduoAsyncProtocolArk {
		t.Fatalf("protocol = %q, claimed = %v; want ark", protocol, claimed)
	}
	if _, claimed := ApiduoAsyncModelProtocol("unclaimed-model"); claimed {
		t.Fatal("an unclaimed model must not report a protocol")
	}
}
