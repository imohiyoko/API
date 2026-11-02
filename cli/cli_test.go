package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imohiyoko/API/cli"
	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/manifest"
)

func TestCheckExitCodes(t *testing.T) {
	m, err := users.Registry(&users.Store{}).Manifest()
	if err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	// 例には警告（gRPC が未実装）が1つある。
	if code := cli.Run(m, []string{"check"}, &out, &errw); code != 0 {
		t.Fatalf("check: %d %s", code, out.String())
	}
	if code := cli.Run(m, []string{"check", "-strict"}, &out, &errw); code != 1 {
		t.Fatalf("check -strict: %d", code)
	}
	bad := &manifest.Manifest{Version: manifest.Version, Operations: []manifest.Operation{{Name: "Bad", Kind: "query"}}}
	out.Reset()
	if code := cli.Run(bad, []string{"check"}, &out, &errw); code != 1 || !strings.Contains(out.String(), "error:") {
		t.Fatalf("bad check: %d %s", code, out.String())
	}
}

func TestGenWritesOneDocumentPerAudience(t *testing.T) {
	m, _ := users.Registry(&users.Store{}).Manifest()
	dir := t.TempDir()
	var out, errw bytes.Buffer
	if code := cli.Run(m, []string{"gen", "-out", dir}, &out, &errw); code != 0 {
		t.Fatalf("gen: %d %s", code, errw.String())
	}
	for _, f := range []string{"manifest.json", "openapi/internal.json", "openapi/partner.json", "openapi/public.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
}
