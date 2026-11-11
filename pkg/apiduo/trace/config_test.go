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

// TestConsoleBaseFallsBackToHost pins the jump-link contract: the export
// endpoint is often an internal container name (apiduo-langfuse-web:3000)
// that a browser cannot resolve, so the console URL must be settable on its
// own and must default to HOST when unset.
func TestConsoleBaseFallsBackToHost(t *testing.T) {
	t.Setenv("LANGFUSE_HOST", "http://apiduo-langfuse-web:3000/")
	t.Setenv("LANGFUSE_CONSOLE_URL", "")
	c := loadFromEnv()
	if c.Host != "http://apiduo-langfuse-web:3000" {
		t.Fatalf("host must lose the trailing slash, got %q", c.Host)
	}
	if got := c.ConsoleBase(); got != "http://apiduo-langfuse-web:3000" {
		t.Fatalf("empty console url must fall back to host, got %q", got)
	}

	t.Setenv("LANGFUSE_CONSOLE_URL", "https://langfuse.1task.cn/")
	c = loadFromEnv()
	if got := c.ConsoleBase(); got != "https://langfuse.1task.cn" {
		t.Fatalf("console url must win and lose the trailing slash, got %q", got)
	}
	if c.Host != "http://apiduo-langfuse-web:3000" {
		t.Fatalf("console url must not change the export endpoint, got %q", c.Host)
	}
}

// The full-mode cap used to be a compile-time constant; it is console-editable
// now, which makes "0" a value a settings row can actually deliver. Zero must
// never reach the collector as a byte limit: a 0-byte cap keeps nothing while
// the console still says full capture, the worst possible failure shape.
func TestBodyMaxBytesFallsBackAndClamps(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"unset keeps the compiled default", 0, defaultFullMaxBytes},
		{"negative is not a cap", -1, defaultFullMaxBytes},
		{"below the floor is raised", 1, MinBodyFullBytes},
		{"in range is kept", 8 << 20, 8 << 20},
		{"above the ceiling is lowered", 1 << 30, MaxBodyFullBytes},
	}
	for _, tc := range cases {
		if got := ClampBodyFullBytes(tc.in); got != tc.want {
			t.Fatalf("%s: ClampBodyFullBytes(%d) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
		if got := (Config{FullMaxBytes: tc.in}).BodyMaxBytes(); got != tc.want {
			t.Fatalf("%s: BodyMaxBytes() with %d = %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}

	t.Setenv("LANGFUSE_BODY_FULL_MAX_BYTES", "8388608")
	if got := EnvDefaultBodyFullCap(); got != 8<<20 {
		t.Fatalf("env value must win, got %d", got)
	}
	t.Setenv("LANGFUSE_BODY_FULL_MAX_BYTES", "not-a-number")
	if got := EnvDefaultBodyFullCap(); got != defaultFullMaxBytes {
		t.Fatalf("garbage must fall back to the default, got %d", got)
	}
}
