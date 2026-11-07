package system_setting

import (
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// Apiduo appearance styles.
//
// These are the shapes the stylesheet in web/src/styles/apiduo-appearance.css
// knows how to draw. The list is closed on purpose: a value the stylesheet does
// not implement would leave apiduo surfaces with no effect at all, which is
// worse than falling back to the default.
const (
	ApiduoAppearanceStyleFlow      = "flow"
	ApiduoAppearanceStyleSpotlight = "spotlight"
	ApiduoAppearanceStyleSheen     = "sheen"
	ApiduoAppearanceStylePulse     = "pulse"
	ApiduoAppearanceStyleRainbow   = "rainbow"
	ApiduoAppearanceStyleNone      = "none"
)

// ApiduoAppearanceStyles is the closed set above, in the order the console
// lists it.
var ApiduoAppearanceStyles = []string{
	ApiduoAppearanceStyleFlow,
	ApiduoAppearanceStyleSpotlight,
	ApiduoAppearanceStyleSheen,
	ApiduoAppearanceStylePulse,
	ApiduoAppearanceStyleRainbow,
	ApiduoAppearanceStyleNone,
}

// Apiduo holiday skins.
//
// A holiday skin is a site-wide palette: it replaces the accent colour the
// whole console is drawn with, not just the decoration on apiduo surfaces.
// The ids deliberately match the effect presets on the console so an operator
// picks "Spring Festival" once instead of coordinating two lists.
var ApiduoHolidays = []string{
	ApiduoHolidaySpringFestival,
	ApiduoHolidayMidAutumn,
	ApiduoHolidayNationalDay,
	ApiduoHolidayHalloween,
	ApiduoHolidayChristmas,
}

const (
	ApiduoHolidaySpringFestival = "spring-festival"
	ApiduoHolidayMidAutumn      = "mid-autumn"
	ApiduoHolidayNationalDay    = "national-day"
	ApiduoHolidayHalloween      = "halloween"
	ApiduoHolidayChristmas      = "christmas"
)

const (
	apiduoAppearanceMinSpeedMS = 600
	apiduoAppearanceMaxSpeedMS = 20000
	apiduoAppearanceMinRingPX  = 0.5
	apiduoAppearanceMaxRingPX  = 6
	apiduoAppearanceMinTailDeg = 10
	apiduoAppearanceMaxTailDeg = 350
)

// ApiduoAppearanceSetting is the decoration apiduo paints on its own surfaces.
//
// Every field is a plain scalar. The console writes one option row per field,
// so a half-filled form keeps the untouched fields instead of replacing the
// whole configuration with a template default. Numeric bounds are enforced in
// Normalize rather than only in the form: an operator can write these rows
// directly, and a value of 0ms would make the animation spin at an unbounded
// rate.
type ApiduoAppearanceSetting struct {
	// Enabled is the master switch. Off renders exactly what upstream renders.
	Enabled bool `json:"enabled"`
	// Style picks the shape. Empty means "not configured yet" and resolves to
	// flow, which is what the channel picker already ships.
	Style string `json:"style"`
	// Color is a CSS color. Empty means "follow the theme" — the effect then
	// tracks --primary, so a theme change carries over without a second edit.
	Color string `json:"color"`
	// SpeedMS is one full revolution, in milliseconds.
	SpeedMS int `json:"speed_ms"`
	// RingPX is the visible stroke width.
	RingPX float64 `json:"ring_px"`
	// TailDeg is the angular length of the travelling highlight.
	TailDeg int `json:"tail_deg"`
	// Glow adds a drop-shadow around the highlight.
	Glow bool `json:"glow"`
	// Holiday names a site-wide palette. Empty means "no holiday".
	Holiday string `json:"holiday"`
	// HolidayFrom and HolidayTo bound the window as MM-DD, so one set of rows
	// can be prepared in advance and left in place across years. Both empty
	// means the holiday is on whenever Holiday names one; the browser does the
	// date comparison because only it knows the visitor's local date.
	HolidayFrom string `json:"holiday_from"`
	HolidayTo   string `json:"holiday_to"`
}

const apiduoAppearanceSettingKey = "apiduo_appearance_setting"

var defaultApiduoAppearanceSetting = ApiduoAppearanceSetting{
	Enabled: true,
	Style:   ApiduoAppearanceStyleFlow,
	SpeedMS: 3200,
	RingPX:  1.5,
	TailDeg: 120,
	Glow:    true,
}

func init() {
	config.GlobalConfig.Register(apiduoAppearanceSettingKey, &defaultApiduoAppearanceSetting)
}

var apiduoAppearanceOnce sync.Once

// GetApiduoAppearanceSetting returns the live configuration.
//
// Persisted option rows are replayed on first use because registration alone
// only covers live edits: an admin update reaches the struct by reflection, but
// nothing restores the rows at boot.
func GetApiduoAppearanceSetting() *ApiduoAppearanceSetting {
	apiduoAppearanceOnce.Do(func() {
		common.OptionMapRWMutex.RLock()
		options := make(map[string]string, 8)
		const prefix = apiduoAppearanceSettingKey + "."
		for key, value := range common.OptionMap {
			if field, ok := strings.CutPrefix(key, prefix); ok {
				options[field] = value
			}
		}
		common.OptionMapRWMutex.RUnlock()

		if len(options) == 0 {
			return
		}
		if err := config.UpdateConfigFromMap(&defaultApiduoAppearanceSetting, options); err != nil {
			common.SysError(common.LogText("failed to load apiduo appearance setting: %s", err.Error()))
			return
		}
		defaultApiduoAppearanceSetting.Normalize()
	})
	return &defaultApiduoAppearanceSetting
}

// Normalize pulls every field into a range the stylesheet can render.
//
// It is idempotent and runs both after a load and before the public copy is
// built, so a value written straight into the option table cannot reach the
// browser as-is.
func (s *ApiduoAppearanceSetting) Normalize() {
	if !isApiduoAppearanceStyle(s.Style) {
		s.Style = ApiduoAppearanceStyleFlow
	}
	if s.SpeedMS < apiduoAppearanceMinSpeedMS || s.SpeedMS > apiduoAppearanceMaxSpeedMS {
		s.SpeedMS = defaultApiduoAppearanceSetting.SpeedMS
	}
	if s.RingPX < apiduoAppearanceMinRingPX || s.RingPX > apiduoAppearanceMaxRingPX {
		s.RingPX = defaultApiduoAppearanceSetting.RingPX
	}
	if s.TailDeg < apiduoAppearanceMinTailDeg || s.TailDeg > apiduoAppearanceMaxTailDeg {
		s.TailDeg = defaultApiduoAppearanceSetting.TailDeg
	}
	// An unset colour must stay unset: nil and empty both mean "follow the
	// theme", and rewriting it here would freeze the effect to today's palette.
	s.Color = strings.TrimSpace(s.Color)

	if !isApiduoHoliday(s.Holiday) {
		s.Holiday = ""
	}
	s.HolidayFrom = normalizeHolidayDate(s.HolidayFrom)
	s.HolidayTo = normalizeHolidayDate(s.HolidayTo)
	// A half-open window is a typo, not an intent. Clearing both makes the
	// holiday behave as "always on", which is what an operator who only filled
	// in one end almost certainly wanted.
	if s.HolidayFrom == "" || s.HolidayTo == "" {
		s.HolidayFrom = ""
		s.HolidayTo = ""
	}
}

// normalizeHolidayDate accepts MM-DD and returns "" for anything else.
//
// Month and day only, so a single row survives into next year without an
// operator remembering to bump it.
func normalizeHolidayDate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if _, err := time.Parse("01-02", value); err != nil {
		return ""
	}
	return value
}

func isApiduoHoliday(holiday string) bool {
	for _, candidate := range ApiduoHolidays {
		if candidate == holiday {
			return true
		}
	}
	return false
}

func isApiduoAppearanceStyle(style string) bool {
	for _, candidate := range ApiduoAppearanceStyles {
		if candidate == style {
			return true
		}
	}
	return false
}

// ApiduoAppearanceSnapshot is what the browser receives.
//
// It is a separate type rather than the setting struct so that a future admin
// only field cannot leak into an endpoint that answers without authentication.
type ApiduoAppearanceSnapshot struct {
	Enabled bool    `json:"enabled"`
	Style   string  `json:"style"`
	Color   string  `json:"color"`
	SpeedMS int     `json:"speed_ms"`
	RingPX  float64 `json:"ring_px"`
	TailDeg int     `json:"tail_deg"`
	Glow    bool    `json:"glow"`
	Holiday string  `json:"holiday"`
	// The window stays in the reply even though the browser decides whether it
	// is open: the console shows it, and a client that ignores it still gets a
	// holiday name rather than silence.
	HolidayFrom string `json:"holiday_from"`
	HolidayTo   string `json:"holiday_to"`
}

// Snapshot returns the normalized public view.
func (s ApiduoAppearanceSetting) Snapshot() ApiduoAppearanceSnapshot {
	normalized := s
	normalized.Normalize()
	if !normalized.Enabled {
		// The disabled shape carries no style so a stale cached stylesheet
		// cannot decorate anything even if a client ignores the flag.
		return ApiduoAppearanceSnapshot{Enabled: false, Style: ApiduoAppearanceStyleNone}
	}
	return ApiduoAppearanceSnapshot{
		Enabled:     true,
		Style:       normalized.Style,
		Color:       normalized.Color,
		SpeedMS:     normalized.SpeedMS,
		RingPX:      normalized.RingPX,
		TailDeg:     normalized.TailDeg,
		Glow:        normalized.Glow,
		Holiday:     normalized.Holiday,
		HolidayFrom: normalized.HolidayFrom,
		HolidayTo:   normalized.HolidayTo,
	}
}
