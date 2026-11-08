package trace

// Snapshot is the immutable unit that crosses the isolation boundary
// (docs §3.4 ①): the relay defer builds it once, and after that the request
// goroutine never touches the data again. Fixed fields only — no maps, no
// pointers into request-scoped storage.
type Snapshot struct {
	TraceID      string
	SpanID       string
	ParentSpanID string // empty unless the client sent a valid traceparent
	RequestID    string

	Operation string // "chat" / "task" / ...
	Name      string // "chat gpt-4o"
	Model     string
	Group     string
	TokenName string
	Format    string // relay format name

	UserId    int
	ChannelId int
	TokenId   int

	Success     bool
	ErrorStatus int    // upstream HTTP status when failed, 0 otherwise
	ErrorText   string // masked, human-readable failure reason

	StartUnixNano int64
	EndUnixNano   int64
	TtftMs        int64
	LatencyMs     int64
	IsStream      bool
	Interrupted   bool // failed after partial frames were received

	PromptTokens     int
	CompletionTokens int
	Quota            int
	Retry            int

	HasContent bool
	Input      []byte
	Output     []byte

	InputTruncated  bool
	OutputTruncated bool
	InputDropped    int
	OutputDropped   int
}
