package dto

import "testing"

// 协议是客户端形状的开关，不是自由文本：写错一个值会让整条渠道在运行时
// 走错分支，所以在保存期就归一化并拒绝未知值。
func TestAdvancedAsyncProtocolValidation(t *testing.T) {
	config := validAdvancedAsyncConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if config.Protocol != AdvancedAsyncProtocolCustom {
		t.Fatalf("protocol = %q, want %q by default", config.Protocol, AdvancedAsyncProtocolCustom)
	}

	config.Protocol = "Images"
	if err := config.Validate(); err != nil {
		t.Fatalf("protocol must be case insensitive: %v", err)
	}
	if config.Protocol != AdvancedAsyncProtocolImages {
		t.Fatalf("protocol = %q, want images", config.Protocol)
	}

	config.Protocol = "sora"
	if err := config.Validate(); err == nil {
		t.Fatal("unknown protocol must be rejected")
	}
}

// 8001 只做异步：同步入口既不能被任何协议声明为入站，也不能在存量配置里
// 被匹配到 —— 同步请求一旦选中一条没有同步适配器的渠道，客户端会拿到任务号
// 或者直接失败。
func TestAdvancedAsyncRejectsSyncEntriesForEveryProtocol(t *testing.T) {
	for _, protocol := range []string{
		AdvancedAsyncProtocolImages,
		AdvancedAsyncProtocolArk,
		AdvancedAsyncProtocolCustom,
	} {
		for _, path := range []string{
			AdvancedAsyncSyncImagesPath,
			AdvancedAsyncSyncImagesEditsPath,
		} {
			config := validAdvancedAsyncConfig()
			config.Protocol = protocol
			config.IncomingPaths = []string{path}
			if err := config.Validate(); err == nil {
				t.Fatalf("%s protocol must not claim the synchronous endpoint %s", protocol, path)
			}
		}
	}

	config := validAdvancedAsyncConfig()
	config.IncomingPaths = []string{"/v1/images/generations/async"}
	if err := config.Validate(); err != nil {
		t.Fatalf("dedicated async path must be accepted: %v", err)
	}
	if config.SupportsIncomingPath(AdvancedAsyncSyncImagesPath) {
		t.Fatal("a sync entry must never match, even when an old config lists it")
	}
	if !config.SupportsIncomingPath("/v1/images/generations/async") {
		t.Fatal("dedicated async path must match")
	}
}

func TestAdvancedAsyncNormalizedProtocol(t *testing.T) {
	cases := []struct {
		config *AdvancedAsyncConfig
		want   string
	}{
		{nil, AdvancedAsyncProtocolCustom},
		{&AdvancedAsyncConfig{}, AdvancedAsyncProtocolCustom},
		{&AdvancedAsyncConfig{Protocol: " IMAGES "}, AdvancedAsyncProtocolImages},
		{&AdvancedAsyncConfig{Protocol: "Ark"}, AdvancedAsyncProtocolArk},
		{&AdvancedAsyncConfig{Protocol: "custom"}, AdvancedAsyncProtocolCustom},
		{&AdvancedAsyncConfig{Protocol: "unknown"}, AdvancedAsyncProtocolCustom},
	}
	for _, testCase := range cases {
		if got := testCase.config.NormalizedProtocol(); got != testCase.want {
			t.Fatalf("%+v: NormalizedProtocol() = %q, want %q", testCase.config, got, testCase.want)
		}
	}
	if (&AdvancedAsyncConfig{Protocol: AdvancedAsyncProtocolImages}).IsImageProtocol() != true {
		t.Fatal("IsImageProtocol must be true for images")
	}
	// 渠道没配 advanced_async 时是 nil：受理回包那条分支会直接调它，
	// 不能在这里炸掉整个提交。
	if (*AdvancedAsyncConfig)(nil).IsImageProtocol() != false {
		t.Fatal("IsImageProtocol must be false on a nil config")
	}
}

// 入站路径可以按业务分段：/jimeng/v1/images/generations 与
// /hailuo/v1/images/generations 各归各的渠道。这类路径不是同步入口，必须
// 被接受；尾斜杠要在保存时抹平，否则精确匹配永远对不上。
func TestAdvancedAsyncAcceptsBizScopedIncomingPaths(t *testing.T) {
	config := validAdvancedAsyncConfig()
	config.Protocol = AdvancedAsyncProtocolImages
	config.IncomingPaths = []string{
		" /jimeng/v1/images/generations ",
		"/hailuo/v1/images/generations/",
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("biz-scoped paths must be accepted: %v", err)
	}
	if !config.SupportsIncomingPath("/jimeng/v1/images/generations") {
		t.Fatal("biz-scoped path must match")
	}
	if !config.SupportsIncomingPath("/hailuo/v1/images/generations") {
		t.Fatal("a trailing slash must be trimmed at save time, not at match time")
	}
	if config.SupportsIncomingPath(AdvancedAsyncSyncImagesPath) {
		t.Fatal("the synchronous endpoint must stay unreachable")
	}
	if config.SupportsIncomingPath("/other/v1/images/generations") {
		t.Fatal("the biz segment is literal, not a wildcard")
	}
}
