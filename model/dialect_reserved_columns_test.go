package model

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// SQLite treats key and group as ordinary identifiers, so a query that selects
// them unquoted passes the entire unit-test suite and then fails on MySQL 8 the
// first time production runs it:
//
//	Error 1064: ... near 'key FROM `tokens` WHERE id IN (...)' at line 1
//
// Quoting must come from commonKeyCol / commonGroupCol, which pick the right
// quote character per dialect. This test reads the source instead of querying a
// database, because no SQLite-backed test can observe the difference.
var sqlCallPattern = regexp.MustCompile(
	`\b(Select|Where|Order|Group|Pluck|Distinct|Having|Joins|Table|Update)\s*\(\s*"([^"]*)"`)

// A bare column is one delimited by SQL syntax on both sides; `key`, "key",
// token_key and apiduo_appearance_setting.key are all fine.
var bareReservedColumnPattern = regexp.MustCompile(`(^|[ ,(])(key|group)([ ,)]|$)`)

// These take a column name and hand it to clause.Column, which quotes it for
// the active dialect. Their argument is only raw SQL once it carries a comma or
// a space — Select("id, name, key") is the shape that broke production, while
// Update("group", value) is safe.
var columnNameArgumentMethods = map[string]bool{
	"Select":   true,
	"Pluck":    true,
	"Update":   true,
	"Distinct": true,
}

func TestQueriesQuoteReservedColumns(t *testing.T) {
	// The packages that build SQL by hand.
	dirs := []string{".", "../controller", "../service"}
	checked := 0

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			checked++
			for lineNumber, line := range strings.Split(string(body), "\n") {
				if strings.Contains(line, "commonKeyCol") || strings.Contains(line, "commonGroupCol") {
					continue
				}
				for _, match := range sqlCallPattern.FindAllStringSubmatch(line, -1) {
					method, snippet := match[1], match[2]
					if !bareReservedColumnPattern.MatchString(snippet) {
						continue
					}
					if columnNameArgumentMethods[method] && !strings.ContainsAny(snippet, ", ") {
						continue
					}
					t.Errorf(
						"%s:%d %s(\"%s\") passes a reserved column name through raw SQL; use commonKeyCol/commonGroupCol",
						path, lineNumber+1, method, snippet,
					)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("reserved-column linter scanned no files")
	}
}
