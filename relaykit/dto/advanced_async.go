package dto

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// AdvancedAsyncConfig describes a generic submit-then-poll upstream for an
// Advanced Custom channel (type 58). The channel keeps its synchronous routes
// in advanced_custom; this block describes the vendor's task API: where to
// submit, where to poll, and how to read the task id, status and result out of
// each reply.
//
// It is intentionally data-only. Adding an upstream means editing this JSON,
// not writing an adaptor.
type AdvancedAsyncConfig struct {
	// Protocol selects the built-in client shape this channel speaks:
	//
	//	"images"  submit answers a task id, GET <incoming>/{id} answers a
	//	          standard Images response once the task is terminal;
	//	"ark"     the Volcengine Ark task shape, the client polls the task
	//	          object itself;
	//	"custom"  no built-in shape, the fields below describe everything.
	//
	// 它只固定**形状**与默认值，不固定地址：上下游端点仍然逐条可改，
	// 因为同一套形状在不同供应商那里的路径并不一致。默认 custom。
	Protocol string `json:"protocol,omitempty"`
	// IncomingPaths are the client paths this channel serves through its task
	// API. The distributor matches them before the synchronous route table, so
	// an async channel does not need a dummy sync route for the same path.
	IncomingPaths []string             `json:"incoming_paths,omitempty"`
	Submit        AdvancedAsyncSubmit  `json:"submit"`
	Query         AdvancedAsyncQuery   `json:"query"`
	Fields        AdvancedAsyncFields  `json:"fields"`
	Billing       AdvancedAsyncBilling `json:"billing,omitempty"`
}

// AdvancedAsyncSubmit is the vendor endpoint that creates a task.
type AdvancedAsyncSubmit struct {
	// UpstreamPath is a path starting with / (joined with the channel base
	// URL) or a full http(s) URL. It may contain {model}, replaced with the
	// upstream model name.
	UpstreamPath string `json:"upstream_path"`
	// Method defaults to POST.
	Method string `json:"method,omitempty"`
	// Auth defaults to the channel's Bearer api key.
	Auth *AdvancedCustomRouteAuth `json:"auth,omitempty"`
	// ContentType is "json" (default) or "multipart". Multipart forwards the
	// client request body as multipart/form-data.
	ContentType string `json:"content_type,omitempty"`
}

// AdvancedAsyncQuery is the vendor endpoint that reports task state.
type AdvancedAsyncQuery struct {
	// UpstreamPath is a path starting with / or a full http(s) URL. A
	// {task_id} placeholder is replaced in place; otherwise the id is sent as
	// TaskIDParam in the location named by TaskIDLocation.
	UpstreamPath string `json:"upstream_path"`
	// Method defaults to GET.
	Method string `json:"method,omitempty"`
	// TaskIDParam defaults to task_id.
	TaskIDParam string `json:"task_id_param,omitempty"`
	// TaskIDLocation is "query" (default) or "path".
	TaskIDLocation string `json:"task_id_location,omitempty"`
	// Auth defaults to the channel's Bearer api key.
	Auth *AdvancedCustomRouteAuth `json:"auth,omitempty"`
}

// AdvancedAsyncFields tells the gateway where to find values in vendor JSON.
// Each entry is a path of object keys and array indexes, for example
// ["data", "task", "id"] or ["output", "0", "url"].
type AdvancedAsyncFields struct {
	TaskIDPath    []string `json:"task_id_path"`
	StatusPath    []string `json:"status_path"`
	ResultURLPath []string `json:"result_url_path,omitempty"`
	ProgressPath  []string `json:"progress_path,omitempty"`
	ErrorPath     []string `json:"error_path,omitempty"`
	// CompletionTokensPath and TotalTokensPath locate the vendor's token counts
	// in the query reply. "per_token" billing settles from them; leaving both
	// empty keeps the task on its reserved quota.
	CompletionTokensPath []string `json:"completion_tokens_path,omitempty"`
	TotalTokensPath      []string `json:"total_tokens_path,omitempty"`
	// StatusMap maps a vendor status (as written in the reply) to one of
	// NOT_START / SUBMITTED / QUEUED / IN_PROGRESS / SUCCESS / FAILURE /
	// UNKNOWN. When empty, common vendor words are recognized.
	StatusMap map[string]string `json:"status_map,omitempty"`
}

// AdvancedAsyncBilling selects how the submitted task is charged.
type AdvancedAsyncBilling struct {
	// Mode is "per_call" (default), "per_second" or "per_token". It applies to
	// every model this channel serves unless Models overrides it.
	//
	// per_call   the model price is charged once, whatever the task produced.
	// per_second the model price is read as the price of one second and
	//            multiplied by the requested duration.
	// per_token  the reservation is the model-ratio estimate and the task is
	//            settled from the vendor's token counts on completion, which
	//            means the model must carry a ModelRatio, not a ModelPrice.
	Mode string `json:"mode,omitempty"`
	// Models maps a model name to its own mode, so one channel can bill one
	// model per call, another per second and a third per token. A model absent
	// here falls back to Mode.
	Models map[string]string `json:"models,omitempty"`
	// DurationPath locates the requested seconds in the client request body.
	// Defaults to ["duration"], which is where the Ark task shape writes it.
	DurationPath []string `json:"duration_path,omitempty"`
	// DefaultSeconds applies when the request carries no readable duration.
	// Zero leaves the reservation at the plain per-call price.
	DefaultSeconds float64 `json:"default_seconds,omitempty"`
	// TokenFallbackPrice is a per-call price charged when a per-token task
	// reports no token counts, so the task never stays at the ratio estimate.
	// Zero keeps the reservation untouched.
	TokenFallbackPrice float64 `json:"token_fallback_price,omitempty"`
}

// ModeFor returns the billing mode that applies to this model, falling back to
// the channel-wide Mode and then to per_call. Model names match case
// insensitively because clients spell them inconsistently.
func (c *AdvancedAsyncBilling) ModeFor(modelName string) string {
	if c == nil {
		return AdvancedAsyncBillingPerCall
	}
	modelName = strings.TrimSpace(modelName)
	if modelName != "" {
		if mode, ok := c.Models[modelName]; ok && strings.TrimSpace(mode) != "" {
			return strings.ToLower(strings.TrimSpace(mode))
		}
		for name, mode := range c.Models {
			if strings.EqualFold(strings.TrimSpace(name), modelName) && strings.TrimSpace(mode) != "" {
				return strings.ToLower(strings.TrimSpace(mode))
			}
		}
	}
	if mode := strings.TrimSpace(c.Mode); mode != "" {
		return strings.ToLower(mode)
	}
	return AdvancedAsyncBillingPerCall
}

const (
	AdvancedAsyncBillingPerCall   = "per_call"
	AdvancedAsyncBillingPerSecond = "per_second"
	AdvancedAsyncBillingPerToken  = "per_token"

	AdvancedAsyncContentTypeJSON      = "json"
	AdvancedAsyncContentTypeMultipart = "multipart"

	AdvancedAsyncTaskIDQuery = "query"
	AdvancedAsyncTaskIDPath  = "path"

	AdvancedAsyncProtocolImages = "images"
	AdvancedAsyncProtocolArk    = "ark"
	AdvancedAsyncProtocolCustom = "custom"

	// AdvancedAsyncSyncImagesPath is the synchronous OpenAI Images endpoint.
	// Advanced-async channels (type 8001) never serve synchronous traffic, so
	// no protocol may claim it as an inbound entry: a channel that did would be
	// picked by the distributor's path filter for plain synchronous requests
	// and then fail, because the type has no synchronous adaptor.
	AdvancedAsyncSyncImagesPath = "/v1/images/generations"

	// AdvancedAsyncSyncImagesEditsPath is the synchronous Images edit endpoint,
	// reserved for the same reason.
	AdvancedAsyncSyncImagesEditsPath = "/v1/images/edits"

	advancedAsyncDefaultTaskIDParam = "task_id"
)

// IsAdvancedAsyncReservedSyncPath reports whether the path belongs to the
// synchronous relay.
//
// Type 8001 is async-only: it has no synchronous adaptor, so it must never
// answer such a path. The distributor's path filter would otherwise pick an
// async channel for a plain synchronous request as soon as one of them listed
// the path as its inbound entry.
func IsAdvancedAsyncReservedSyncPath(requestPath string) bool {
	switch strings.TrimSpace(requestPath) {
	case AdvancedAsyncSyncImagesPath, AdvancedAsyncSyncImagesEditsPath:
		return true
	default:
		return false
	}
}

// SupportsIncomingPath reports whether the client path is served by the task
// API. The distributor matches it before the synchronous route table.
//
// 路径可以是任意形状，包括按业务分段的一级前缀：
//
//	/v1/images/generations/async    默认形状，与同步入口区分
//	/jimeng/v1/images/generations   按业务分段，biz 段由运营在渠道里写死
//
// 匹配是逐字相等（只有 {model} 当占位符），biz 段不是通配符：即梦的渠道
// 只接 /jimeng/...，不会把海螺的流量一起接走。
func (c *AdvancedAsyncConfig) SupportsIncomingPath(requestPath string) bool {
	if c == nil || requestPath == "" {
		return false
	}
	// Older configs may still list a sync entry; never let it match.
	if IsAdvancedAsyncReservedSyncPath(requestPath) {
		return false
	}
	for _, incoming := range c.IncomingPaths {
		// Share the synchronous route matcher so {model} templates and the
		// Gemini :generateContent equivalence behave identically in both
		// route tables.
		if matchAdvancedCustomIncomingPath(strings.TrimSpace(incoming), requestPath) {
			return true
		}
	}
	return false
}

// NormalizedProtocol returns the protocol name with the empty value folded to
// custom, so configs written before the field existed keep working.
func (c *AdvancedAsyncConfig) NormalizedProtocol() string {
	if c == nil {
		return AdvancedAsyncProtocolCustom
	}
	switch protocol := strings.ToLower(strings.TrimSpace(c.Protocol)); protocol {
	case AdvancedAsyncProtocolImages, AdvancedAsyncProtocolArk:
		return protocol
	default:
		return AdvancedAsyncProtocolCustom
	}
}

// IsImageProtocol reports whether the channel speaks the built-in Images shape.
func (c *AdvancedAsyncConfig) IsImageProtocol() bool {
	return c.NormalizedProtocol() == AdvancedAsyncProtocolImages
}

// IsEnabled reports whether the channel carries an async configuration.
func (c *AdvancedAsyncConfig) IsEnabled() bool {
	return c != nil && (c.Submit.UpstreamPath != "" || c.Query.UpstreamPath != "")
}

// Validate rejects an async configuration the driver cannot execute. It must
// run at save time so a broken channel never receives traffic.
func (c *AdvancedAsyncConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.Submit.UpstreamPath == "" && c.Query.UpstreamPath == "" {
		return fmt.Errorf("advanced_async requires submit.upstream_path and query.upstream_path")
	}
	switch protocol := strings.ToLower(strings.TrimSpace(c.Protocol)); protocol {
	case "":
		c.Protocol = AdvancedAsyncProtocolCustom
	case AdvancedAsyncProtocolImages, AdvancedAsyncProtocolArk, AdvancedAsyncProtocolCustom:
		c.Protocol = protocol
	default:
		return fmt.Errorf("advanced_async.protocol is not supported: %s", c.Protocol)
	}
	for index, incoming := range c.IncomingPaths {
		trimmed := strings.TrimSpace(incoming)
		if !strings.HasPrefix(trimmed, "/") {
			return fmt.Errorf("advanced_async.incoming_paths[%d] must start with /", index)
		}
		if strings.Contains(trimmed, "?") {
			return fmt.Errorf("advanced_async.incoming_paths[%d] must not include query", index)
		}
		// 入站路径除非带 {model} 否则是逐字相等：尾斜杠会让
		// /jimeng/v1/images/generations/ 与 /jimeng/v1/images/generations
		// 变成两条不同的路径——保存时不报错，客户端却永远拿不到任务号。
		if trimmed != "/" {
			trimmed = strings.TrimRight(trimmed, "/")
		}
		if trimmed == "" || strings.Contains(trimmed, "//") {
			return fmt.Errorf("advanced_async.incoming_paths[%d] is not a valid path", index)
		}
		c.IncomingPaths[index] = trimmed
		if IsAdvancedAsyncReservedSyncPath(trimmed) {
			return fmt.Errorf("advanced_async.incoming_paths must not reuse the synchronous endpoint %s", trimmed)
		}
	}

	submitMethod, err := normalizeAdvancedAsyncMethod(c.Submit.Method, "POST", []string{"POST", "PUT", "PATCH"})
	if err != nil {
		return fmt.Errorf("advanced_async.submit.%w", err)
	}
	c.Submit.Method = submitMethod
	contentType := strings.ToLower(strings.TrimSpace(c.Submit.ContentType))
	switch contentType {
	case "":
		c.Submit.ContentType = AdvancedAsyncContentTypeJSON
	case AdvancedAsyncContentTypeJSON, AdvancedAsyncContentTypeMultipart:
		c.Submit.ContentType = contentType
	default:
		return fmt.Errorf("advanced_async.submit.content_type must be json or multipart: %s", c.Submit.ContentType)
	}
	if err := validateAdvancedAsyncTarget("submit", c.Submit.UpstreamPath); err != nil {
		return err
	}
	if err := validateAdvancedCustomRouteAuth(0, c.Submit.Auth); err != nil {
		return err
	}

	queryMethod, err := normalizeAdvancedAsyncMethod(c.Query.Method, "GET", []string{"GET", "POST"})
	if err != nil {
		return fmt.Errorf("advanced_async.query.%w", err)
	}
	c.Query.Method = queryMethod
	if err := validateAdvancedAsyncTarget("query", c.Query.UpstreamPath); err != nil {
		return err
	}
	location := strings.ToLower(strings.TrimSpace(c.Query.TaskIDLocation))
	switch location {
	case "":
		c.Query.TaskIDLocation = AdvancedAsyncTaskIDQuery
	case AdvancedAsyncTaskIDQuery, AdvancedAsyncTaskIDPath:
		c.Query.TaskIDLocation = location
	default:
		return fmt.Errorf("advanced_async.query.task_id_location must be query or path: %s", c.Query.TaskIDLocation)
	}
	if c.Query.TaskIDParam == "" {
		c.Query.TaskIDParam = advancedAsyncDefaultTaskIDParam
	}
	if c.Query.TaskIDLocation == AdvancedAsyncTaskIDPath && !strings.Contains(c.Query.UpstreamPath, "{task_id}") {
		return fmt.Errorf("advanced_async.query.upstream_path must contain {task_id} when task_id_location is path")
	}
	if err := validateAdvancedCustomRouteAuth(0, c.Query.Auth); err != nil {
		return err
	}

	if len(c.Fields.TaskIDPath) == 0 {
		return fmt.Errorf("advanced_async.fields.task_id_path is required")
	}
	if len(c.Fields.StatusPath) == 0 {
		return fmt.Errorf("advanced_async.fields.status_path is required")
	}
	for name, path := range map[string][]string{
		"task_id_path":    c.Fields.TaskIDPath,
		"status_path":     c.Fields.StatusPath,
		"result_url_path": c.Fields.ResultURLPath,
		"progress_path":   c.Fields.ProgressPath,
		"error_path":      c.Fields.ErrorPath,
	} {
		if err := validateAdvancedAsyncFieldPath(name, path); err != nil {
			return err
		}
	}
	for vendorStatus, gatewayStatus := range c.Fields.StatusMap {
		if strings.TrimSpace(vendorStatus) == "" {
			return fmt.Errorf("advanced_async.fields.status_map contains an empty vendor status")
		}
		if !IsAdvancedAsyncTaskStatus(gatewayStatus) {
			return fmt.Errorf("advanced_async.fields.status_map[%s] is not a task status: %s", vendorStatus, gatewayStatus)
		}
	}

	mode := strings.ToLower(strings.TrimSpace(c.Billing.Mode))
	switch mode {
	case "":
		c.Billing.Mode = AdvancedAsyncBillingPerCall
	case AdvancedAsyncBillingPerCall, AdvancedAsyncBillingPerSecond, AdvancedAsyncBillingPerToken:
		c.Billing.Mode = mode
	default:
		return fmt.Errorf("advanced_async.billing.mode is not supported: %s", c.Billing.Mode)
	}
	if c.Billing.DefaultSeconds < 0 {
		return fmt.Errorf("advanced_async.billing.default_seconds must not be negative")
	}
	if c.Billing.TokenFallbackPrice < 0 {
		return fmt.Errorf("advanced_async.billing.token_fallback_price must not be negative")
	}
	for name, mode := range c.Billing.Models {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("advanced_async.billing.models has an empty model name")
		}
		normalized := strings.ToLower(strings.TrimSpace(mode))
		switch normalized {
		case AdvancedAsyncBillingPerCall, AdvancedAsyncBillingPerSecond, AdvancedAsyncBillingPerToken:
			c.Billing.Models[name] = normalized
		default:
			return fmt.Errorf("advanced_async.billing.models[%s] is not supported: %s", name, mode)
		}
	}
	return nil
}

// IsAdvancedAsyncTaskStatus reports whether value is a gateway task status.
func IsAdvancedAsyncTaskStatus(value string) bool {
	switch value {
	case "NOT_START", "SUBMITTED", "QUEUED", "IN_PROGRESS", "SUCCESS", "FAILURE", "UNKNOWN":
		return true
	default:
		return false
	}
}

// NormalizeAdvancedAsyncStatus maps a vendor status to a gateway task status.
// Configured entries win; otherwise common vendor vocabulary is recognized.
// An unrecognized value is UNKNOWN, which the poller treats as non-terminal.
func (f AdvancedAsyncFields) NormalizeAdvancedAsyncStatus(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "UNKNOWN"
	}
	for vendorStatus, gatewayStatus := range f.StatusMap {
		if strings.EqualFold(strings.TrimSpace(vendorStatus), trimmed) {
			return gatewayStatus
		}
	}
	switch strings.ToLower(trimmed) {
	case "success", "succeeded", "completed", "complete", "done", "finished":
		return "SUCCESS"
	case "failure", "failed", "fail", "error", "cancelled", "canceled", "expired":
		return "FAILURE"
	case "queued", "pending", "waiting", "created":
		return "QUEUED"
	case "running", "processing", "in_progress", "inprogress", "started", "generating":
		return "IN_PROGRESS"
	case "submitted":
		return "SUBMITTED"
	default:
		return "UNKNOWN"
	}
}

// LookupAdvancedAsyncPath reads a nested value out of decoded vendor JSON.
// Object keys and array indexes are both addressed by path segment.
func LookupAdvancedAsyncPath(root any, path []string) (any, bool) {
	current := root
	for _, segment := range path {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return nil, false
		}
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return nil, false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// AdvancedAsyncStringAt reads a path and renders the value as a string.
func AdvancedAsyncStringAt(root any, path []string) (string, bool) {
	value, ok := LookupAdvancedAsyncPath(root, path)
	if !ok || value == nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed)), true
	}
}

func normalizeAdvancedAsyncMethod(method string, fallback string, allowed []string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(method))
	if normalized == "" {
		return fallback, nil
	}
	for _, candidate := range allowed {
		if normalized == candidate {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("method must be one of %s: %s", strings.Join(allowed, ", "), method)
}

func validateAdvancedAsyncTarget(field string, target string) error {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return fmt.Errorf("advanced_async.%s.upstream_path is required", field)
	}
	if strings.HasPrefix(trimmed, "/") {
		if strings.HasPrefix(trimmed, "//") {
			return fmt.Errorf("advanced_async.%s.upstream_path must be a full URL or a path starting with /", field)
		}
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("advanced_async.%s.upstream_path must be a full URL or a path starting with /", field)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("advanced_async.%s.upstream_path must use http or https", field)
	}
	return nil
}

func validateAdvancedAsyncFieldPath(name string, path []string) error {
	for index, segment := range path {
		if strings.TrimSpace(segment) == "" {
			return fmt.Errorf("advanced_async.fields.%s[%d] is empty", name, index)
		}
	}
	return nil
}
