package system_setting

import "testing"

func TestApiduoAppearanceNormalizeClampsEveryField(t *testing.T) {
	setting := ApiduoAppearanceSetting{
		Enabled: true,
		Style:   "sparkles",
		SpeedMS: 1,
		RingPX:  99,
		TailDeg: 0,
	}
	setting.Normalize()

	if setting.Style != ApiduoAppearanceStyleFlow {
		t.Fatalf("unknown style must fall back to flow, got %q", setting.Style)
	}
	if setting.SpeedMS != defaultApiduoAppearanceSetting.SpeedMS {
		t.Fatalf("speed must fall back to the default, got %d", setting.SpeedMS)
	}
	if setting.RingPX != defaultApiduoAppearanceSetting.RingPX {
		t.Fatalf("ring must fall back to the default, got %v", setting.RingPX)
	}
	if setting.TailDeg != defaultApiduoAppearanceSetting.TailDeg {
		t.Fatalf("tail must fall back to the default, got %d", setting.TailDeg)
	}
}

func TestApiduoAppearanceNormalizeKeepsEmptyColor(t *testing.T) {
	// Empty means "follow the theme". Filling it in here would freeze the
	// effect to whichever palette was current on the day it was saved.
	setting := ApiduoAppearanceSetting{Style: ApiduoAppearanceStylePulse, Color: "   "}
	setting.Normalize()
	if setting.Color != "" {
		t.Fatalf("blank colour must stay empty, got %q", setting.Color)
	}
}

func TestApiduoAppearanceSnapshotDropsStyleWhenDisabled(t *testing.T) {
	setting := ApiduoAppearanceSetting{
		Enabled: false,
		Style:   ApiduoAppearanceStyleRainbow,
		Color:   "#f00",
		SpeedMS: 2000,
		RingPX:  2,
		TailDeg: 90,
	}
	snapshot := setting.Snapshot()

	if snapshot.Enabled {
		t.Fatal("disabled setting must report disabled")
	}
	if snapshot.Style != ApiduoAppearanceStyleNone {
		t.Fatalf("disabled snapshot must not name a style, got %q", snapshot.Style)
	}
	if snapshot.Color != "" || snapshot.SpeedMS != 0 || snapshot.RingPX != 0 || snapshot.TailDeg != 0 {
		t.Fatalf("disabled snapshot must be blank, got %+v", snapshot)
	}
}

func TestApiduoAppearanceSnapshotNormalizesEnabled(t *testing.T) {
	setting := ApiduoAppearanceSetting{
		Enabled: true,
		Style:   "nope",
		SpeedMS: 3200,
		RingPX:  1.5,
		TailDeg: 120,
	}
	snapshot := setting.Snapshot()

	if !snapshot.Enabled || snapshot.Style != ApiduoAppearanceStyleFlow {
		t.Fatalf("expected a flow snapshot, got %+v", snapshot)
	}
	// Snapshot must not mutate the caller's copy.
	if setting.Style != "nope" {
		t.Fatalf("Snapshot must not write back to the setting, got %q", setting.Style)
	}
}

func TestApiduoAppearanceDefaultsAreSteady(t *testing.T) {
	if !defaultApiduoAppearanceSetting.Enabled {
		t.Fatal("the effect ships on: apiduo surfaces are identified by it")
	}
	if defaultApiduoAppearanceSetting.Style != ApiduoAppearanceStyleFlow {
		t.Fatalf("default style must be flow, got %q", defaultApiduoAppearanceSetting.Style)
	}
	if defaultApiduoAppearanceSetting.Color != "" {
		t.Fatal("default colour must follow the theme")
	}
}
