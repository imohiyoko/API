// Package archtest はモジュール境界を検査する（design.md 2.3：コアは adapters/ を import しない）。
package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
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

// heavyDeps は、使う利用者だけがビルドに含める依存（design.md 2.3）。gRPC は runtime/grpcx、YAML は manifest/yamlx だけが import する。
var heavyDeps = []string{"google.golang.org/grpc", "google.golang.org/genproto", "google.golang.org/protobuf", "go.yaml.in/yaml"}

func isHeavy(p string) bool {
	for _, d := range heavyDeps {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// define・manifest・runtime・port（サブパッケージを除く）は grpc・protobuf・yaml を import しない。
func TestCoreDoesNotImportGRPCOrYAML(t *testing.T) {
	for _, dir := range []string{"define", "manifest", "runtime", "port"} {
		root := filepath.Join("..", "..", dir)
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(root, name)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if isHeavy(p) || p == "github.com/imohiyoko/API/gen/proto" {
					t.Errorf("%s imports %s", path, p)
				}
			}
		}
	}
}

// REST だけを使う例のビルド（間接の依存も含む）に grpc・protobuf・yaml が出ない。
func TestRESTOnlyBuildHasNoGRPCOrYAML(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not found")
	}
	for _, pkg := range []string{"./examples/users/cmd/users-rest", "./runtime", "./manifest", "./define", "./port"} {
		cmd := exec.Command(gobin, "list", "-deps", pkg)
		cmd.Dir = filepath.Join("..", "..")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
		}
		for _, p := range strings.Fields(string(out)) {
			if isHeavy(p) {
				t.Errorf("%s depends on %s", pkg, p)
			}
		}
	}
}
