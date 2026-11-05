// Command api は、マニフェストのファイル（Go 以外で書いたもの、YAML も）を検査し、契約ファイルを書き出し、
// サイドカーとして Go 以外の本体の隣で動く（design.md 2.2、ADR 0003）。
//
//	api -manifest api/manifest.yaml check
//	api -manifest api/manifest.yaml gen -out api
//	api -manifest api/manifest.yaml sidecar -upstream http://127.0.0.1:3000 -listen internal=127.0.0.1:8090,public=:8092
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/imohiyoko/API/cli"
	"github.com/imohiyoko/API/manifest"
)

func main() {
	path := flag.String("manifest", "manifest.json", "マニフェストのファイル（.yaml・.yml なら YAML、それ以外は JSON）")
	flag.Parse()
	f, err := os.Open(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m, err := manifest.ReadFile(*path, f)
	f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if args := flag.Args(); len(args) > 0 && args[0] == "sidecar" {
		os.Exit(sidecarMain(m, args[1:]))
	}
	os.Exit(cli.Run(m, flag.Args(), os.Stdout, os.Stderr))
}
