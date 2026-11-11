package constant

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// webTaskActionListPath is the frontend mirror of the task-action enum. Keeping
// them in step matters: the log table renders the action through a lookup table,
// so a renamed backend value silently degrades every row to "Unknown".
var webTaskActionListPath = filepath.Join(
	"..", "web", "src", "features", "usage-logs", "constants.ts",
)

var webTaskActionEntry = regexp.MustCompile(`[A-Z_][A-Z0-9_]*\s*:\s*'([^']+)'`)

// TestWebTaskActionListCoversBackend guards the rename that broke the task log
// once already: every backend action value must exist in the frontend table.
func TestWebTaskActionListCoversBackend(t *testing.T) {
	backend := backendTaskActionValues(t)
	if len(backend) == 0 {
		t.Fatal("no TaskAction constants found in task.go")
	}

	raw, err := os.ReadFile(webTaskActionListPath)
	if err != nil {
		t.Skipf("frontend sources are unavailable: %v", err)
	}
	frontend := webTaskActionValues(string(raw))
	if len(frontend) == 0 {
		t.Fatalf("no entries parsed from %s", webTaskActionListPath)
	}

	for _, action := range backend {
		if _, ok := frontend[action]; !ok {
			t.Errorf("task action %q is missing from the frontend TASK_ACTIONS table (%s)", action, webTaskActionListPath)
		}
	}
}

// backendTaskActionValues reads the declared values straight out of task.go so
// a newly added action cannot be forgotten here.
func backendTaskActionValues(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "task.go", nil, 0)
	if err != nil {
		t.Fatalf("parse task.go: %v", err)
	}
	var values []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Values) == 0 {
			return true
		}
		for i, name := range spec.Names {
			if len(name.Name) < len("TaskAction") || name.Name[:len("TaskAction")] != "TaskAction" {
				continue
			}
			if i >= len(spec.Values) {
				continue
			}
			literal, ok := spec.Values[i].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			if value, err := strconv.Unquote(literal.Value); err == nil && value != "" {
				values = append(values, value)
			}
		}
		return true
	})
	return values
}

// webTaskActionValues collects the values inside the TASK_ACTIONS object. The
// block is located by its declaration and closed at the "} as const" line, so
// neighbouring tables with the same shape are not mixed in.
func webTaskActionValues(source string) map[string]struct{} {
	start := regexp.MustCompile(`export const TASK_ACTIONS = \{`).FindStringIndex(source)
	if start == nil {
		return nil
	}
	rest := source[start[1]:]
	end := regexp.MustCompile(`\n\}\s*as const`).FindStringIndex(rest)
	if end == nil {
		return nil
	}
	values := make(map[string]struct{})
	for _, match := range webTaskActionEntry.FindAllStringSubmatch(rest[:end[0]], -1) {
		values[match[1]] = struct{}{}
	}
	return values
}
