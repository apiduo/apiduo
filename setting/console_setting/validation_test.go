/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package console_setting

import (
	"strings"
	"testing"
)

// announcementsJSON wraps a phases expression (a raw JSON fragment) into one
// announcement. An empty phases argument omits the field entirely, which is how
// every announcement stored before the timeline existed looks.
func announcementsJSON(phases string) string {
	const head = `[{"content":"测试公告","publishDate":"2026-11-16T10:00:00+08:00"`
	if phases == "" {
		return head + `}]`
	}
	return head + `,"phases":` + phases + `}]`
}

func TestValidateAnnouncementsPhases(t *testing.T) {
	eight := `[` + strings.Repeat(`{"at":"2026-11-10T00:00:00+08:00","title":"阶段"},`, 7) +
		`{"at":"2026-11-10T00:00:00+08:00","title":"阶段"}]`
	nine := `[` + strings.Repeat(`{"at":"2026-11-10T00:00:00+08:00","title":"阶段"},`, 8) +
		`{"at":"2026-11-10T00:00:00+08:00","title":"阶段"}]`

	cases := []struct {
		name    string
		phases  string
		wantErr string
	}{
		{name: "字段不存在（存量数据）", phases: ""},
		{name: "空数组", phases: `[]`},
		{name: "单个阶段", phases: `[{"at":"2026-11-10T00:00:00+08:00","title":"代码冻结"}]`},
		{name: "带描述", phases: `[{"at":"2026-11-10T00:00:00+08:00","title":"代码冻结","desc":"切出 rc.1 分支"}]`},
		{name: "八个阶段是上限", phases: eight},
		{name: "不是数组", phases: `"oops"`, wantErr: "phases must be an array"},
		{name: "元素不是对象", phases: `["2026-11-10"]`, wantErr: "must be an object"},
		{name: "超过八个", phases: nine, wantErr: "cannot have more than 8 phases"},
		{name: "缺少 at", phases: `[{"title":"a"}]`, wantErr: "missing the at field"},
		{name: "at 不是 RFC3339", phases: `[{"at":"11-10","title":"a"}]`, wantErr: "invalid at format"},
		{name: "缺少 title", phases: `[{"at":"2026-11-10T00:00:00+08:00"}]`, wantErr: "missing the title field"},
		{
			name:    "title 超长",
			phases:  `[{"at":"2026-11-10T00:00:00+08:00","title":"` + strings.Repeat("字", 61) + `"}]`,
			wantErr: "title cannot exceed 60",
		},
		{
			name:    "desc 超长",
			phases:  `[{"at":"2026-11-10T00:00:00+08:00","title":"a","desc":"` + strings.Repeat("字", 201) + `"}]`,
			wantErr: "description cannot exceed 200",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConsoleSettings(announcementsJSON(tc.phases), "Announcements")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}
