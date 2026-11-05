package users_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/manifest"
)

// roadmap.md M3：YAML で書いたマニフェストと、同じ内容を Go で書いた定義の書き出しが一致する。
func TestYAMLManifestMatchesGoDefinition(t *testing.T) {
	fromGo, err := users.Registry(&users.Store{}).Manifest()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("api/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fromYAML, err := manifest.ReadFile("api/manifest.yaml", f)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := manifest.Marshal(fromGo)
	got, _ := manifest.Marshal(fromYAML)
	if !bytes.Equal(got, want) {
		t.Fatalf("manifest.yaml differs from the Go definition:\n--- yaml\n%s\n--- go\n%s", got, want)
	}
}
