// APIDUO:billing-factors:BEGIN
package dto

import (
	"fmt"
	"strings"
)

// AdvancedAsyncBillingFactor multiplies a task's price by the value the client
// sent at Path, looked up in Values.
//
// The fixed billing modes express time (per_second) but nothing else, so a
// vendor that charges 720P and 1080P at different rates had no way to bill what
// it actually paid. A factor makes the price follow any request field.
//
// The result joins the same pre-charge ratio map as "seconds", so the final
// price is ModelPrice × seconds × factor. A factor whose path is absent,
// unreadable or unmatched falls back to Default (one when unset) and never
// blocks the task.
type AdvancedAsyncBillingFactor struct {
	// Key names the ratio in the estimate log, for example "resolution".
	Key string `json:"key"`
	// Path locates the client value in the request body, for example
	// ["resolution"] or ["metadata", "size"].
	Path []string `json:"path"`
	// Values maps each accepted client spelling to its multiplier. Matching
	// trims surrounding space and ignores ASCII case, because clients disagree
	// on 720P versus 720p.
	Values map[string]float64 `json:"values"`
	// Default applies to an absent, unreadable or unmatched value. Zero is
	// treated as one so an unset factor can never make a task free.
	Default float64 `json:"default,omitempty"`
}

// MultiplierOf resolves this factor against a decoded request body. The second
// return value is the raw client spelling, which the caller logs so an operator
// can see why a tier did or did not match; it is empty when nothing was read.
// The last return value reports whether Values supplied the multiplier.
func (f AdvancedAsyncBillingFactor) MultiplierOf(requestBody any) (multiplier float64, raw string, matched bool) {
	value, ok := AdvancedAsyncStringAt(requestBody, f.Path)
	if !ok || value == "" {
		return f.effectiveDefault(), "", false
	}
	for spelling, candidate := range f.Values {
		if strings.EqualFold(strings.TrimSpace(spelling), value) {
			return candidate, value, true
		}
	}
	return f.effectiveDefault(), value, false
}

func (f AdvancedAsyncBillingFactor) effectiveDefault() float64 {
	if f.Default > 0 {
		return f.Default
	}
	return 1
}

// ValidateAdvancedAsyncBillingFactors rejects a factor the driver cannot apply.
// It runs at save time so a malformed table never reaches a live channel.
func ValidateAdvancedAsyncBillingFactors(factors []AdvancedAsyncBillingFactor) error {
	keys := make(map[string]struct{}, len(factors))
	for index, factor := range factors {
		key := strings.TrimSpace(factor.Key)
		if key == "" {
			return fmt.Errorf("advanced_async.billing.factors[%d].key is required", index)
		}
		if _, duplicate := keys[key]; duplicate {
			return fmt.Errorf("advanced_async.billing.factors[%d].key is duplicated: %s", index, key)
		}
		keys[key] = struct{}{}
		if len(factor.Path) == 0 {
			return fmt.Errorf("advanced_async.billing.factors[%s].path is required", key)
		}
		if len(factor.Values) == 0 {
			return fmt.Errorf("advanced_async.billing.factors[%s].values is required", key)
		}
		for spelling, multiplier := range factor.Values {
			if strings.TrimSpace(spelling) == "" {
				return fmt.Errorf("advanced_async.billing.factors[%s].values has an empty key", key)
			}
			if multiplier < 0 {
				return fmt.Errorf("advanced_async.billing.factors[%s].values[%s] must not be negative", key, spelling)
			}
		}
		if factor.Default < 0 {
			return fmt.Errorf("advanced_async.billing.factors[%s].default must not be negative", key)
		}
	}
	return nil
}

// APIDUO:billing-factors:END
