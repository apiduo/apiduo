package types

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

// T3 判据 1：额度不足的错误必须带一条可执行的出路。
// 客户看到 403 时最需要知道的是「钱花完了，去哪充」，
// 而不是一句 cryptic 的 insufficient quota。
func TestQuotaErrorCarriesTopUp(t *testing.T) {
	err := NewErrorWithStatusCode(
		errors.New("quota exhausted"),
		ErrorCodeInsufficientUserQuota,
		403,
		ErrOptionWithTopUp(TopUpHint{URL: "/wallet", Balance: kitutil.GetPointer(3.75)}),
	)
	raw, marshalErr := json.Marshal(err.ToOpenAIError())
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}
	// ToOpenAIError 返回的就是要写进响应体的那个对象本身，
	// 外层是否包 error 由调用方决定，判据不该假设它被包过。
	var got struct {
		Code  string `json:"code"`
		TopUp *struct {
			URL     string   `json:"url"`
			Balance *float64 `json:"balance"`
		} `json:"topup"`
	}
	if unmarshalErr := json.Unmarshal(raw, &got); unmarshalErr != nil {
		t.Fatalf("unmarshal: %v", unmarshalErr)
	}
	if got.Code != string(ErrorCodeInsufficientUserQuota) {
		t.Errorf("code = %q", got.Code)
	}
	if got.TopUp == nil {
		t.Fatalf("额度错误缺少 topup 字段: %s", raw)
	}
	if got.TopUp.URL != "/wallet" {
		t.Errorf("topup.url = %q", got.TopUp.URL)
	}
	if got.TopUp.Balance == nil || *got.TopUp.Balance != 3.75 {
		t.Errorf("topup.balance = %v", got.TopUp.Balance)
	}
}

// T3 判据 2：普通错误不能被污染。加字段的前提是它只出现在额度类错误上，
// 否则所有错误响应都会多一块无意义的内容。
func TestNormalErrorHasNoTopUp(t *testing.T) {
	err := NewErrorWithStatusCode(
		errors.New("bad request"),
		ErrorCodeInvalidRequest,
		400,
	)
	raw, _ := json.Marshal(err.ToOpenAIError())
	if strings.Contains(string(raw), "topup") {
		t.Errorf("非额度错误不应包含 topup: %s", raw)
	}
}

// T3 判据 3：即使调用方没显式传 option，额度类错误也必须带出路。
// 额度拒绝点有九处，靠人记着逐处加必然漏；兜底必须在序列化层。
func TestQuotaErrorAutoCarriesTopUp(t *testing.T) {
	for _, code := range []ErrorCode{ErrorCodeInsufficientUserQuota, ErrorCodePreConsumeTokenQuotaFailed} {
		err := NewErrorWithStatusCode(errors.New("no quota"), code, 403)
		oe := err.ToOpenAIError()
		if oe.TopUp == nil {
			t.Errorf("%s 未自动挂 topup", code)
			continue
		}
		if oe.TopUp.URL != DefaultTopUpPath {
			t.Errorf("%s topup.url = %q", code, oe.TopUp.URL)
		}
	}
}
