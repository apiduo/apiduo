package model

import (
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

func benchmarkStickyChannel(keyCount int) *Channel {
	keys := make([]string, keyCount)
	for i := range keyCount {
		keys[i] = "sk-bench-" + strconv.Itoa(i) + "-" + strings.Repeat("x", 40)
	}
	channel := &Channel{Key: strings.Join(keys, "\n")}
	channel.ChannelInfo.IsMultiKey = true
	channel.ChannelInfo.MultiKeySize = keyCount
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeSticky
	return channel
}

func benchmarkStickyChannelWithParsedKeys(keyCount int) *Channel {
	channel := benchmarkStickyChannel(keyCount)
	channel.Keys = channel.GetKeys()
	return channel
}

func BenchmarkStickyKeyScore(b *testing.B) {
	seed := "token:12345"
	key := "sk-bench-0-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	b.ReportAllocs()
	for b.Loop() {
		_ = stickyKeyScore(seed, key)
	}
}

func BenchmarkGetStickyEnabledKey4(b *testing.B) {
	channel := benchmarkStickyChannel(4)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _, _ = channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
	}
}

func BenchmarkGetStickyEnabledKey16(b *testing.B) {
	channel := benchmarkStickyChannel(16)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _, _ = channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
	}
}

func BenchmarkGetStickyEnabledKey4Parsed(b *testing.B) {
	channel := benchmarkStickyChannelWithParsedKeys(4)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _, _ = channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), nil)
	}
}

func BenchmarkGetNextEnabledKeyRandom4(b *testing.B) {
	channel := benchmarkStickyChannel(4)
	channel.ChannelInfo.MultiKeyMode = constant.MultiKeyModeRandom
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = channel.GetNextEnabledKey()
	}
}

func BenchmarkGetStickyWithRetrySkip(b *testing.B) {
	channel := benchmarkStickyChannel(8)
	skip := map[int]struct{}{0: {}, 1: {}, 2: {}}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _, _ = channel.GetStickyEnabledKey("token:"+strconv.Itoa(i), skip)
	}
}

func BenchmarkGetStickyEnabledKey3000(b *testing.B) {
	channel := benchmarkStickyChannelWithParsedKeys(3000)
	seeds := make([]string, 1024)
	for i := range seeds {
		seeds[i] = "token:" + strconv.Itoa(i)
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _, _ = channel.GetStickyEnabledKey(seeds[i&1023], nil)
	}
}

func BenchmarkGetStickyEnabledKey10000(b *testing.B) {
	channel := benchmarkStickyChannelWithParsedKeys(10000)
	seeds := make([]string, 1024)
	for i := range seeds {
		seeds[i] = "token:" + strconv.Itoa(i)
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _, _ = channel.GetStickyEnabledKey(seeds[i&1023], nil)
	}
}

func BenchmarkGetStickyEnabledKey3000HalfDisabled(b *testing.B) {
	channel := benchmarkStickyChannelWithParsedKeys(3000)
	disabled := make(map[int]int, 1500)
	for i := 0; i < 3000; i += 2 {
		disabled[i] = common.ChannelStatusAutoDisabled
	}
	channel.ChannelInfo.MultiKeyStatusList = disabled
	seeds := make([]string, 1024)
	for i := range seeds {
		seeds[i] = "token:" + strconv.Itoa(i)
	}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _, _ = channel.GetStickyEnabledKey(seeds[i&1023], nil)
	}
}
