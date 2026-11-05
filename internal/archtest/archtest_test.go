// Package archtest はモジュール境界を検査する（design.md 2.3：コアは adapters/ を import しない）。
package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCoreDoesNotImportAdapters(t *testing.T) {
	for _, dir := range []string{"define", "manifest", "runtime", "port", "gen", "cli", "sidecar"} {
		root := filepath.Join("..", "..", dir)
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if strings.HasPrefix(p, "github.com/imohiyoko/API/adapters") || strings.HasPrefix(p, "github.com/imohiyoko/API/examples") {
					t.Errorf("%s imports %s", path, p)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
