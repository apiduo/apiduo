package main

import (
	"testing"
)

func TestMarkedLinesCountsBlockBody(t *testing.T) {
	content := []byte(`a
/* APIDUO:feature-one:BEGIN */
b
c
/* APIDUO:feature-one:END */
d
`)

	inside, err := markedLines(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, number := range []int{2, 3, 4, 5} {
		if _, ok := inside[number]; !ok {
			t.Errorf("第 %d 行应在块内", number)
		}
	}
	for _, number := range []int{1, 6} {
		if _, ok := inside[number]; ok {
			t.Errorf("第 %d 行应在块外", number)
		}
	}
}

func TestMarkedLinesAcceptsNestedBlocks(t *testing.T) {
	content := []byte(`/* APIDUO:outer:BEGIN */
a
/* APIDUO:inner:BEGIN */
b
/* APIDUO:inner:END */
c
/* APIDUO:outer:END */
`)

	inside, err := markedLines(content)
	if err != nil {
		t.Fatalf("嵌套块应当合法: %v", err)
	}
	if len(inside) != 7 {
		t.Errorf("七行都应在块内，实际 %d 行", len(inside))
	}
}

func TestMarkedLinesRejectsMismatchedEnd(t *testing.T) {
	// 最常见的笔误：复制块时改了 BEGIN 忘了改 END。
	content := []byte(`/* APIDUO:first:BEGIN */
a
/* APIDUO:second:END */
`)

	if _, err := markedLines(content); err == nil {
		t.Fatal("feature 名不一致时应当报错")
	}
}

func TestMarkedLinesRejectsUnclosedBlock(t *testing.T) {
	content := []byte(`/* APIDUO:first:BEGIN */
a
`)

	if _, err := markedLines(content); err == nil {
		t.Fatal("未闭合的块应当报错")
	}
}

func TestMarkedLinesRejectsStrayEnd(t *testing.T) {
	content := []byte(`a
/* APIDUO:first:END */
`)

	if _, err := markedLines(content); err == nil {
		t.Fatal("孤立的 END 应当报错")
	}
}

func TestOutsideLinesIgnoresBlankLines(t *testing.T) {
	// 块与块之间留空行是排版惯例，不该算成冲突面。
	content := []byte(`one
/* APIDUO:feature-one:BEGIN */
two
/* APIDUO:feature-one:END */

three
`)

	inside, err := markedLines(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 行号 1..7：第 5、7 行是空行（第二个来自结尾换行），第 2-4 行在块内。
	outside := outsideLines(content, []int{1, 2, 3, 4, 5, 6, 7}, inside)
	if len(outside) != 2 {
		t.Fatalf("只应报出第 1、6 两个非空行，实际 %v", outside)
	}
	if outside[0] != 1 || outside[1] != 6 {
		t.Errorf("报出的行号不对: %v", outside)
	}
}

func TestSkipByFormat(t *testing.T) {
	skipped := []string{
		"web/src/i18n/locales/zh.json",
		"go.sum",
		"web/bun.lock",
		"web/src/routeTree.gen.ts",
		"docs/logo.svg",
	}
	for _, file := range skipped {
		if !skipByFormat(file) {
			t.Errorf("%s 应当跳过", file)
		}
	}

	checked := []string{
		"main.go",
		"web/src/hooks/use-sidebar-view.ts",
		"controller/nav_app.go",
		"docker-compose.yml",
	}
	for _, file := range checked {
		if skipByFormat(file) {
			t.Errorf("%s 不应当跳过", file)
		}
	}
}
