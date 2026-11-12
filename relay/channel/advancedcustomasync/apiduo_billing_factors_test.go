// APIDUO:billing-factors:BEGIN
package advancedcustomasync

import (
	"testing"

	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
)

// resolutionFactor is the shape an operator writes to price 1080P above 720P.
func resolutionFactor() kitdto.AdvancedAsyncBillingFactor {
	return kitdto.AdvancedAsyncBillingFactor{
		Key:    "resolution",
		Path:   []string{"resolution"},
		Values: map[string]float64{"720P": 1, "1080P": 4.0 / 3.0},
	}
}

func TestEstimateBillingMultipliesResolutionFactor(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:    kitdto.AdvancedAsyncBillingPerSecond,
		Factors: []kitdto.AdvancedAsyncBillingFactor{resolutionFactor()},
	}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"happyhorse11","duration":5,"resolution":"1080P"}`)

	ratios := adaptor.EstimateBilling(nil, nil)
	if ratios["seconds"] != 5 {
		t.Fatalf("ratios = %v, want seconds=5", ratios)
	}
	if got := ratios["resolution"]; got != 4.0/3.0 {
		t.Fatalf("resolution ratio = %v, want 4/3", got)
	}
	// The host multiplies every ratio, so the effective multiplier is 5 × 4/3.
	if got := ratios["seconds"] * ratios["resolution"]; got < 6.66 || got > 6.67 {
		t.Fatalf("effective multiplier = %v, want ≈6.667", got)
	}
}

func TestEstimateBillingDropsFactorAtBaseTier(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:    kitdto.AdvancedAsyncBillingPerSecond,
		Factors: []kitdto.AdvancedAsyncBillingFactor{resolutionFactor()},
	}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"happyhorse11","duration":5,"resolution":"720P"}`)

	ratios := adaptor.EstimateBilling(nil, nil)
	if _, present := ratios["resolution"]; present {
		t.Fatalf("a factor of 1 must not add a ratio: %v", ratios)
	}
}

func TestEstimateBillingFactorIgnoresCase(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:    kitdto.AdvancedAsyncBillingPerSecond,
		Factors: []kitdto.AdvancedAsyncBillingFactor{resolutionFactor()},
	}
	adaptor := testAdaptor(t, config)
	// Clients disagree on 1080P versus 1080p; both must price the same.
	adaptor.submitBody = []byte(`{"model":"happyhorse11","duration":5,"resolution":"1080p"}`)

	if got := adaptor.EstimateBilling(nil, nil)["resolution"]; got != 4.0/3.0 {
		t.Fatalf("resolution ratio = %v, want 4/3", got)
	}
}

func TestEstimateBillingFactorAbsentKeepsPrice(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:    kitdto.AdvancedAsyncBillingPerSecond,
		Factors: []kitdto.AdvancedAsyncBillingFactor{resolutionFactor()},
	}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"happyhorse11","duration":5}`)

	ratios := adaptor.EstimateBilling(nil, nil)
	if len(ratios) != 1 || ratios["seconds"] != 5 {
		t.Fatalf("ratios = %v, want only seconds=5", ratios)
	}
}

func TestEstimateBillingFactorAppliesWithoutPerSecond(t *testing.T) {
	config := testConfig()
	config.Billing = kitdto.AdvancedAsyncBilling{
		Mode:    kitdto.AdvancedAsyncBillingPerCall,
		Factors: []kitdto.AdvancedAsyncBillingFactor{resolutionFactor()},
	}
	adaptor := testAdaptor(t, config)
	adaptor.submitBody = []byte(`{"model":"happyhorse11","resolution":"1080P"}`)

	ratios := adaptor.EstimateBilling(nil, nil)
	if _, present := ratios["seconds"]; present {
		t.Fatalf("per_call must not add seconds: %v", ratios)
	}
	if got := ratios["resolution"]; got != 4.0/3.0 {
		t.Fatalf("resolution ratio = %v, want 4/3", got)
	}
}

func TestEstimateBillingRejectsMalformedFactor(t *testing.T) {
	for name, factor := range map[string]kitdto.AdvancedAsyncBillingFactor{
		"empty key":    {Path: []string{"resolution"}, Values: map[string]float64{"720P": 1}},
		"empty path":   {Key: "resolution", Values: map[string]float64{"720P": 1}},
		"empty values": {Key: "resolution", Path: []string{"resolution"}},
		"negative": {
			Key:    "resolution",
			Path:   []string{"resolution"},
			Values: map[string]float64{"720P": -1},
		},
	} {
		config := testConfig()
		config.Billing = kitdto.AdvancedAsyncBilling{Factors: []kitdto.AdvancedAsyncBillingFactor{factor}}
		if err := config.Validate(); err == nil {
			t.Fatalf("%s: expected a validation error", name)
		}
	}
}

// APIDUO:billing-factors:END
