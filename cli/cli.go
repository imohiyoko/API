// Package cli は、定義から契約ファイルを書き出すコマンドを置く（design.md 3章）。
//
// 定義は利用者のコードにあるので、利用者の小さな main から呼ぶ：
//
//	func main() { os.Exit(cli.Main(registry(), os.Args[1:])) }
//
// マニフェストのファイルだけを扱う cmd/api も、同じ Run を使う。
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/gen/openapi"
	"github.com/imohiyoko/API/manifest"
)

// Main は Registry の定義でコマンドを実行し、終了コードを返す。
func Main(reg *define.Registry, args []string) int {
	m, err := reg.Manifest()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return Run(m, args, os.Stdout, os.Stderr)
}

const usage = `usage:
  check [-strict]                       定義を検査する（エラーがあれば終了コード 1）
  manifest [-o file]                    マニフェストを JSON で書き出す
  gen [-out dir] [-title t] [-version v] 公開範囲ごとの openapi/<audience>.json とマニフェストを書き出す
`

// Run はマニフェストでコマンドを実行し、終了コードを返す。
func Run(m *manifest.Manifest, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		fs.SetOutput(stderr)
		strict := fs.Bool("strict", false, "警告もエラーにする")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		return check(m, *strict, stdout)
	case "manifest":
		fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
		fs.SetOutput(stderr)
		out := fs.String("o", "", "出力先（省略時は標準出力）")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		b, err := manifest.Marshal(m)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *out == "" {
			_, _ = stdout.Write(b)
			return 0
		}
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "gen":
		fs := flag.NewFlagSet("gen", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dir := fs.String("out", "api", "出力先のディレクトリ")
		title := fs.String("title", "API", "OpenAPI の info.title")
		version := fs.String("version", "0.0.0", "OpenAPI の info.version")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if code := check(m, false, stderr); code != 0 {
			return code
		}
		if err := Generate(m, *dir, openapi.Info{Title: *title, Version: *version}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

func check(m *manifest.Manifest, strict bool, w io.Writer) int {
	ds := manifest.Check(m)
	for _, d := range ds {
		fmt.Fprintln(w, d)
	}
	if manifest.HasErrors(ds) || (strict && len(ds) > 0) {
		return 1
	}
	return 0
}

// Generate は dir に manifest.json と openapi/<audience>.json を書く。REST に何も出さない公開範囲の文書は作らず、以前のものがあれば消す。
func Generate(m *manifest.Manifest, dir string, info openapi.Info) error {
	if err := os.MkdirAll(filepath.Join(dir, "openapi"), 0o755); err != nil {
		return err
	}
	b, err := manifest.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		return err
	}
	for _, aud := range m.AudienceList() {
		path := filepath.Join(dir, "openapi", aud+".json")
		if !exposesREST(m, aud) {
			// 以前の gen の文書が残ると、出さなくなった操作が契約に載ったままになる。
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		doc, err := openapi.Generate(m, aud, info)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, doc, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func exposesREST(m *manifest.Manifest, aud string) bool {
	for i := range m.Operations {
		if m.Operations[i].Exposed(aud, manifest.REST) {
			return true
		}
	}
	return false
}
