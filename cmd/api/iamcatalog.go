package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/imohiyoko/API/adapters/iam"
	"github.com/imohiyoko/API/manifest"
)

// iamCatalogMain は、マニフェストの操作の Authz から iam の権限のカタログ（POST /v1/admin/permission-catalogs の本体。
// iam の ADR 0121）を標準出力に書く。接頭辞は adapters/iam の PermissionCatalog（IAM_PERMISSION_CATALOG）と同じにする。
func iamCatalogMain(m *manifest.Manifest, args []string) int {
	fs := flag.NewFlagSet("iam-catalog", flag.ContinueOnError)
	prefix := fs.String("prefix", "", "カタログの接頭辞（必須。例：users-api）")
	name := fs.String("name", "", "表示名（省略時はマニフェストの API の名前）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *prefix == "" {
		fmt.Fprintln(os.Stderr, "iam-catalog: -prefix is required")
		return 2
	}
	c, err := iam.CatalogFromManifest(m, *prefix, *name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b, err := iam.MarshalCatalog(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, _ = os.Stdout.Write(b)
	return 0
}
