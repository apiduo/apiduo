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
	// ContentSampled records whether this span was allowed to carry
	// prompt/answer. False means metadata-only on purpose, not a capture bug.
	ContentSampled bool
	Input          []byte
	Output         []byte

	// InlineMedia holds pictures that were lifted out of Output before the
	// full-mode cap could cut them in half, parked until the flush worker can
	// upload them.
	//
	// Why this exists: the cap is applied when the response arrives, but the
	// upload only happens on the export path. A 3 MB image therefore used to
	// be truncated at 2 MB, which left the JSON unterminated — and the
	// externalizer, which needs a closed string literal to match, skipped it.
	// The result was the worst of both: two megabytes of dead base64 in the
	// trace and no picture. Lifting the bytes out here keeps Output small
	// enough to stay intact while the picture itself still reaches the
	// exporter.
	InlineMedia []InlineMediaBlob

	InputTruncated  bool
	OutputTruncated bool
	InputDropped    int
	OutputDropped   int
}

// InlineMediaBlob is one picture extracted from a payload.
type InlineMediaBlob struct {
	// Slot matches the placeholder left in the payload text.
	Slot int
	// Raw is the decoded image, ready to be re-encoded for the upload.
	Raw []byte
}
