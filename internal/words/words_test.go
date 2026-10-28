// Package words_test checks the words of the Go programs (docs/onboarding.md
// §8): an owner may have only an iPhone, or a Linux computer, as trusted
// devices, so no text a person reads names the Mac as the only one.
package words_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Where a device's own words are picked by its platform.
var exempt = map[string]bool{"pkg/mcpgw/consent.go": true}

func TestNoMacOnlyWords(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if exempt[filepath.ToSlash(rel)] {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Errorf("%s: %v", rel, err)
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, _ := strconv.Unquote(lit.Value)
				for _, w := range []string{"your Mac", "Hermesh.app", "Touch ID"} {
					if strings.Contains(s, w) {
						t.Errorf("%s: %q says %q", fset.Position(lit.Pos()), s, w)
					}
				}
				return true
			})
			return nil
		})
	}
}
