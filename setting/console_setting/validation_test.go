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
	"fmt"
	"strings"
	"testing"
	"time"
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

// announcementsWithTitle wraps a raw JSON member (e.g. `"title":"x"`) into one
// announcement. An empty argument omits the field, which is how announcements
// created before the editor exposed titles look.
func announcementsWithTitle(member string) string {
	const head = `[{"content":"测试公告","publishDate":"2026-11-16T10:00:00+08:00"`
	if member == "" {
		return head + `}]`
	}
	return head + `,` + member + `}]`
}

func TestValidateAnnouncementsTitle(t *testing.T) {
	cases := []struct {
		name    string
		member  string
		wantErr string
	}{
		{name: "字段不存在（存量数据）", member: ""},
		{name: "空字符串", member: `"title":""`},
		{name: "正常标题", member: `"title":"计费单位调整"`},
		{name: "正好 60 字", member: `"title":"` + strings.Repeat("字", 60) + `"`},
		{
			name:    "超过 60 字",
			member:  `"title":"` + strings.Repeat("字", 61) + `"`,
			wantErr: "title cannot exceed 60",
		},
		{
			name:    "不是字符串",
			member:  `"title":123`,
			wantErr: "", // 非字符串按“未提供”处理，不阻断整条公告
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConsoleSettings(announcementsWithTitle(tc.member), "Announcements")
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

func TestValidateAnnouncementsExpiresAt(t *testing.T) {
	const published = "2026-11-16T10:00:00+08:00"
	wrap := func(member string) string {
		return `[{"content":"测试公告","publishDate":"` + published + `",` + member + `}]`
	}

	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "字段不存在", body: `[{"content":"测试公告","publishDate":"` + published + `"}]`},
		{name: "空字符串视为不设置", body: wrap(`"expiresAt":""`)},
		{name: "晚于发布时间", body: wrap(`"expiresAt":"2026-11-20T00:00:00+08:00"`)},
		{
			name:    "早于发布时间",
			body:    wrap(`"expiresAt":"2026-11-10T00:00:00+08:00"`),
			wantErr: "expiry must be later than its publish date",
		},
		{
			name:    "与发布时间相同",
			body:    wrap(`"expiresAt":"` + published + `"`),
			wantErr: "expiry must be later than its publish date",
		},
		{
			name:    "格式非法",
			body:    wrap(`"expiresAt":"2026-11-20"`),
			wantErr: "invalid expiry time",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConsoleSettings(tc.body, "Announcements")
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

func TestGetAnnouncementsWindow(t *testing.T) {
	original := consoleSetting.Announcements
	t.Cleanup(func() { consoleSetting.Announcements = original })

	now := time.Now()
	ago := func(h int) string { return now.Add(-time.Duration(h) * time.Hour).Format(time.RFC3339) }
	ahead := func(h int) string { return now.Add(time.Duration(h) * time.Hour).Format(time.RFC3339) }

	consoleSetting.Announcements = fmt.Sprintf(`[
		{"content":"expired","publishDate":%q,"expiresAt":%q},
		{"content":"active","publishDate":%q,"expiresAt":%q},
		{"content":"no-expiry","publishDate":%q},
		{"content":"scheduled","publishDate":%q},
		{"content":"broken-date","publishDate":"not-a-time"}
	]`, ago(48), ago(24), ago(3), ahead(24), ago(2), ahead(6))

	got := GetAnnouncements()
	titles := make([]string, 0, len(got))
	for _, item := range got {
		titles = append(titles, item["content"].(string))
	}

	// 已过期 → 隐藏；未来发布 → 隐藏；日期解析失败 → 保留（容错）
	want := []string{"no-expiry", "active", "broken-date"}
	if len(titles) != len(want) {
		t.Fatalf("expected %d announcements %v, got %d %v", len(want), want, len(titles), titles)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, titles)
		}
	}
}

func TestGetAnnouncementsKeepsBrokenExpiry(t *testing.T) {
	original := consoleSetting.Announcements
	t.Cleanup(func() { consoleSetting.Announcements = original })

	consoleSetting.Announcements = `[{"content":"typo","publishDate":"2020-01-01T00:00:00Z","expiresAt":"tomorrow"}]`

	got := GetAnnouncements()
	if len(got) != 1 {
		t.Fatalf("a broken expiry must not hide the notice, got %d items", len(got))
	}
}

// announcementsWithCategory 与 announcementsWithTitle 同构：
// category 决定这条消息出现在公告面板还是功能更新横幅，写错就会串台。
func announcementsWithCategory(member string) string {
	const head = `[{"content":"测试公告","publishDate":"2026-11-16T10:00:00+08:00"`
	if member == "" {
		return head + `}]`
	}
	return head + `,` + member + `}]`
}

func TestValidateAnnouncementsCategory(t *testing.T) {
	cases := []struct {
		name    string
		member  string
		wantErr string
	}{
		{name: "字段不存在（存量数据）", member: ""},
		{name: "空字符串视为公告", member: `"category":""`},
		{name: "announcement", member: `"category":"announcement"`},
		{name: "changelog", member: `"category":"changelog"`},
		{
			name:    "未知取值",
			member:  `"category":"news"`,
			wantErr: "invalid category",
		},
		{
			name:    "大小写不匹配",
			member:  `"category":"Changelog"`,
			wantErr: "invalid category",
		},
		{
			name:    "不是字符串",
			member:  `"category":1`,
			wantErr: "invalid category",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConsoleSettings(announcementsWithCategory(tc.member), "Announcements")
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
