package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imohiyoko/API/cli"
	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/gen/openapi"
	"github.com/imohiyoko/API/manifest"
)

func TestCheckExitCodes(t *testing.T) {
	m, err := users.Registry(&users.Store{}).Manifest()
	if err != nil {
		t.Fatal(err)
	}
	var out, errw bytes.Buffer
	if code := cli.Run(m, []string{"check", "-strict"}, &out, &errw); code != 0 {
		t.Fatalf("check -strict: %d %s", code, out.String())
	}
	// 警告（GraphQL が未実装）があると、-strict だけが失敗する。
	m.Operations[0].Expose = append(m.Operations[0].Expose, manifest.Exposure{Audience: manifest.Internal, Protocols: []string{manifest.GraphQL}})
	if code := cli.Run(m, []string{"check"}, &out, &errw); code != 0 {
		t.Fatalf("check: %d %s", code, out.String())
	}
	if code := cli.Run(m, []string{"check", "-strict"}, &out, &errw); code != 1 {
		t.Fatalf("check -strict with a warning: %d", code)
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
	for _, f := range []string{"manifest.json", "openapi/internal.json", "openapi/partner.json", "openapi/public.json", "proto/internal.proto"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
}

func TestGenRemovesStaleDocuments(t *testing.T) {
	m, _ := users.Registry(&users.Store{}).Manifest()
	dir := t.TempDir()
	if err := cli.Generate(m, dir, openapi.Info{}); err != nil {
		t.Fatal(err)
	}
	// public から外すと、前回の public.json は消える。
	for i := range m.Operations {
		var kept []manifest.Exposure
		for _, e := range m.Operations[i].Expose {
			if e.Audience != manifest.Public {
				kept = append(kept, e)
			}
		}
		m.Operations[i].Expose = kept
	}
	if err := cli.Generate(m, dir, openapi.Info{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "openapi", "public.json")); !os.IsNotExist(err) {
		t.Fatalf("public.json should be removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "openapi", "partner.json")); err != nil {
		t.Fatal(err)
	}
}

func TestGenRemovesDocumentsOfRemovedAudiences(t *testing.T) {
	m, _ := users.Registry(&users.Store{}).Manifest()
	dir := t.TempDir()
	stale := []string{filepath.Join(dir, "openapi", "vip.json"), filepath.Join(dir, "proto", "vip.proto")}
	for _, f := range stale {
		_ = os.MkdirAll(filepath.Dir(f), 0o755)
		_ = os.WriteFile(f, []byte("old"), 0o644)
	}
	if err := cli.Generate(m, dir, openapi.Info{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range stale {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s should be removed", f)
		}
	}
}
