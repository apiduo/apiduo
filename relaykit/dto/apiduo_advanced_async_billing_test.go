package dto

import "testing"

func TestAdvancedAsyncBillingModeFor(t *testing.T) {
	billing := &AdvancedAsyncBilling{
		Mode: AdvancedAsyncBillingPerCall,
		Models: map[string]string{
			"video-per-second": "per_second",
			"VIDEO-PER-TOKEN":  "per_token",
		},
	}
	cases := map[string]string{
		"video-per-second": AdvancedAsyncBillingPerSecond,
		"video-per-token":  AdvancedAsyncBillingPerToken,
		"video-per-call":   AdvancedAsyncBillingPerCall,
		"":                 AdvancedAsyncBillingPerCall,
	}
	for modelName, want := range cases {
		if got := billing.ModeFor(modelName); got != want {
			t.Fatalf("ModeFor(%q) = %q, want %q", modelName, got, want)
		}
	}
	if got := (*AdvancedAsyncBilling)(nil).ModeFor("anything"); got != AdvancedAsyncBillingPerCall {
		t.Fatalf("nil billing ModeFor = %q, want per_call", got)
	}
}

func TestAdvancedAsyncBillingRejectsUnknownModelMode(t *testing.T) {
	config := validAdvancedAsyncConfig()
	config.Billing = AdvancedAsyncBilling{Models: map[string]string{"video": "per_hour"}}
	if err := config.Validate(); err == nil {
		t.Fatal("Validate accepted an unsupported per-model mode")
	}
}
