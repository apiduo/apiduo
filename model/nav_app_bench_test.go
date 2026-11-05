package model

import (
	"strings"
	"testing"
)

// NavAppsRevision hashes the whole stored option value, so its cost scales with
// the configuration size. The admin endpoints call it once per read and once per
// write; the render path never does.

func benchRevision(b *testing.B, size int) {
	payload := `[{"slug":"app","name":"` + strings.Repeat("x", size) + `"}]`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = navAppsRevisionOf(payload)
	}
}

func BenchmarkNavAppsRevisionSmall(b *testing.B) { benchRevision(b, 512) }
func BenchmarkNavAppsRevisionLarge(b *testing.B) { benchRevision(b, 100*1024) }
