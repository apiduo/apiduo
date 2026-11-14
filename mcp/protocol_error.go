package mcp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"
)

// toolError 是返回给 agent 的结构化错误。
//
// 只有 message 是给人看的；其余三个字段是给 agent 决策的：
// retryable 决定要不要再发一次，retry_action 决定换什么打法，
// code 让客户端不必解析中文。纯文本错误迫使 agent 去猜，而猜错的代价是计费。
type toolError struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	Retryable    bool   `json:"retryable"`
	RetryAction  string `json:"retry_action,omitempty"`
	RetryAfterMS int    `json:"retry_after_ms,omitempty"`
	Hint         string `json:"hint,omitempty"`
}

// 本地校验的错误码。取值稳定，客户端可以按码分支，不要跟着文案改。
const (
	CodePromptRequired       = "PROMPT_REQUIRED"
	CodePromptTooLong        = "PROMPT_TOO_LONG"
	CodeModelNotFound        = "MODEL_NOT_FOUND"
	CodeKindMismatch         = "KIND_MISMATCH"
	CodeParamNotDeclared     = "PARAM_NOT_DECLARED"
	CodeParamOutOfRange      = "PARAM_OUT_OF_RANGE"
	CodeRefImagesExceed      = "REFERENCE_IMAGES_EXCEEDED"
	CodeRefImagesUnsupported = "REFERENCE_IMAGES_UNSUPPORTED"
	CodeUpstreamError        = "UPSTREAM_ERROR"
	CodeInternal             = "INTERNAL_ERROR"
)

// paramError 是「本地能判定、不需要问上游」的错误。
//
// 与普通 fmt.Errorf 的区别只有一处：它带 code。没有 code 的错误在分类时
// 一律落到 INTERNAL_ERROR，agent 会把它当成服务端故障去重试。
type paramError struct {
	Code string
	Msg  string
}

func (e *paramError) Error() string { return e.Msg }

func userParamErrorf(code, format string, args ...any) error {
	return &paramError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// classifyToolError 把任意错误翻译成 agent 能照做的结构。
//
// 顺序：本地码 → 上游规则表 → 兜底。上游规则来自 mcp_error_rules，
// 之所以不在代码里写死：401/402/429 的处置会随部署与渠道变，运营需要能改。
func classifyToolError(modelId string, err error) *toolError {
	if err == nil {
		return nil
	}
	var pe *paramError
	if errors.As(err, &pe) {
		return &toolError{Code: pe.Code, Message: pe.Msg, Retryable: false, RetryAction: "none"}
	}
	// 这三个是 pickView 的判定结果，不是参数取值问题：给独立码，
	// agent 才能区分「模型名写错」和「参数超范围」，而不是统一当参数错误。
	var km *errKindMismatch
	if errors.As(err, &km) {
		return &toolError{Code: CodeKindMismatch, Message: km.Error(), Retryable: false, RetryAction: "none"}
	}
	var na *errModelNotAvailable
	if errors.As(err, &na) {
		return &toolError{Code: CodeModelNotFound, Message: na.Error(), Retryable: false, RetryAction: "call_models_list"}
	}
	var nk errNoModelOfKind
	if errors.As(err, &nk) {
		return &toolError{Code: CodeModelNotFound, Message: nk.Error(), Retryable: false, RetryAction: "call_models_list"}
	}
	var ue *upError
	if errors.As(err, &ue) {
		if rule := matchErrorRule(modelId, ue.Status, ue.Msg); rule != nil {
			return &toolError{
				Code:         upstreamCode(ue),
				Message:      ue.Error(),
				Retryable:    rule.Retryable,
				RetryAction:  rule.RetryAction,
				RetryAfterMS: rule.RetryAfterMs,
				Hint:         rule.Hint,
			}
		}
		return &toolError{
			Code:        upstreamCode(ue),
			Message:     ue.Error(),
			Retryable:   false,
			RetryAction: "none",
			Hint:        "未匹配到重试规则，按不可重试处理",
		}
	}
	return &toolError{
		Code:        CodeInternal,
		Message:     err.Error(),
		Retryable:   false,
		RetryAction: "none",
	}
}

// upstreamCode 让客户端能按 HTTP 语义分支，而不用匹配 message 文本。
func upstreamCode(ue *upError) string {
	if ue == nil {
		return CodeUpstreamError
	}
	if ue.Code != "" {
		return "UPSTREAM_" + strings.ToUpper(sanitizeCode(ue.Code))
	}
	return "UPSTREAM_" + strconv.Itoa(ue.Status)
}

func sanitizeCode(code string) string {
	var b strings.Builder
	for _, r := range code {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// matchErrorRule 在规则表里找第一条命中的规则。
//
// 模型级优先于全局：模型级规则存在的唯一意义就是覆盖全局，先到先得会让它失效。
// 规则表读不到不是错误 —— 分类退化为「不可重试」，这比让工具调用失败安全。
func matchErrorRule(modelId string, status int, msg string) *ErrorRule {
	if model.DB == nil {
		return nil
	}
	rules, err := ListErrorRules(modelId)
	if err != nil {
		return nil
	}
	var matched *ErrorRule
	for i := range rules {
		rule := &rules[i]
		if !rule.Enabled || !ruleMatches(*rule, status, msg) {
			continue
		}
		if matched == nil || (rule.Scope == "model" && matched.Scope != "model") {
			matched = rule
		}
	}
	return matched
}

func ruleMatches(rule ErrorRule, status int, msg string) bool {
	match := strings.TrimSpace(rule.Match)
	if match == "" {
		return false
	}
	if match == "5xx" {
		return status >= 500 && status <= 599
	}
	if code, err := strconv.Atoi(match); err == nil {
		return status == code
	}
	// 业务码形态（如 task:FAILED）走子串匹配；上游文案大小写不稳定，统一折叠。
	return strings.Contains(strings.ToUpper(msg), strings.ToUpper(match))
}
