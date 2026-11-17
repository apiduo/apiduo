package modelalias

import "testing"

func TestApplyModelMapping(t *testing.T) {
	cases := []struct {
		mapping string
		origin  string
		want    string
		mapped  bool
		wantErr bool
	}{
		{"", "m", "m", false, false},
		{"{}", "m", "m", false, false},
		{`{"m":"v"}`, "m", "v", true, false},
		{`{"m":"a","a":"b"}`, "m", "b", true, false}, // 链式
		{`{"m":"m"}`, "m", "m", false, false},        // 恒等
		{`{"other":"x"}`, "m", "m", false, false},    // 无关
		{`{"m":"a","a":"m"}`, "m", "", false, true},  // 环
		{`not-json`, "m", "", false, true},
	}
	for _, tc := range cases {
		got, mapped, err := ApplyModelMapping(tc.mapping, tc.origin)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err = %v, wantErr %v", tc.mapping, err, tc.wantErr)
			continue
		}
		if tc.wantErr {
			continue
		}
		if got != tc.want || mapped != tc.mapped {
			t.Errorf("%q: got (%q,%v), want (%q,%v)", tc.mapping, got, mapped, tc.want, tc.mapped)
		}
	}
}
