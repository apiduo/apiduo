package trace

import "testing"

// TestArmFromEnvOpensGate pins the bug a local e2e run exposed: Enabled() is a
// bare atomic load, so unless something arms it at process start every request
// early-returns and no span is ever produced. Visiting the admin config
// endpoint must NOT be what makes tracing start working.
func TestArmFromEnvOpensGate(t *testing.T) {
	t.Setenv("LANGFUSE_ENABLED", "true")
	t.Setenv("LANGFUSE_HOST", "http://arm.example")
	t.Setenv("LANGFUSE_PROJECT_ID", "p1")
	t.Cleanup(func() { setConfig(&Config{Enabled: false}) })

	// Force the closed state rather than assuming it: other tests in this
	// package install their own enabled config.
	setConfig(&Config{Enabled: false})
	armFromEnv()
	if !Enabled() {
		t.Fatal("armFromEnv must open the gate; otherwise Begin never runs")
	}
	if got := CurrentConfig(); got.Host != "http://arm.example" || got.ProjectID != "p1" {
		t.Fatalf("config not loaded from env: %+v", got)
	}
}
