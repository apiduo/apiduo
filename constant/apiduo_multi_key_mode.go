package constant

// MultiKeyMode values owned by apiduo.
//
// They live in their own file rather than in multi_key_mode.go so that enum
// stays byte-identical to upstream: adding a mode then costs zero merge
// surface. If upstream ever defines the same name, the build fails loudly
// instead of the two declarations silently diverging.
const (
	// MultiKeyModeSticky maps a request seed to one key, so every request of the
	// same token reaches the same upstream credential and its prompt cache stays
	// warm. Unlike polling, it needs no cursor and no shared state.
	MultiKeyModeSticky MultiKeyMode = "sticky"
)
