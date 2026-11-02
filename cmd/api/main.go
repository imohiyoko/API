// Command api は、マニフェストのファイル（Go 以外で書いたものも）を検査し、契約ファイルを書き出す。
//
//	api -manifest api/manifest.json check
//	api -manifest api/manifest.json gen -out api
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/imohiyoko/API/cli"
	"github.com/imohiyoko/API/manifest"
)

func main() {
	path := flag.String("manifest", "manifest.json", "マニフェストのファイル")
	flag.Parse()
	f, err := os.Open(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m, err := manifest.Read(f)
	f.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(cli.Run(m, flag.Args(), os.Stdout, os.Stderr))
}
