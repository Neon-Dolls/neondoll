package cognitiveexecution_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestProductionInferenceCallsStayBehindCognitiveExecution is an architecture
// guardrail. Provider implementations are free to implement Infer, but a new
// production call to Infer must either enter through CognitiveExecution or be
// an explicitly audited provider-adapter delegation.
func TestProductionInferenceCallsStayBehindCognitiveExecution(t *testing.T) {
	t.Parallel()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	allowed := map[string]struct{}{
		"Core/CognitiveExecution/executor.go": {},
		"Core/Inference/tool_provider.go":     {},
	}

	var bypasses []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, ok := allowed[relative]; ok {
			return nil
		}

		files := token.NewFileSet()
		parsed, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Infer" {
				return true
			}
			position := files.Position(call.Pos())
			bypasses = append(bypasses, relative+":"+strconv.Itoa(position.Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bypasses) > 0 {
		sort.Strings(bypasses)
		t.Fatalf("production inference calls bypass CognitiveExecution or an audited adapter: %v", bypasses)
	}
}
