package openapi_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/gen/openapi"
	"github.com/imohiyoko/API/manifest"
)

func load(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := users.Registry(&users.Store{}).Manifest()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func gen(t *testing.T, m *manifest.Manifest, aud string) map[string]any {
	t.Helper()
	b, err := openapi.Generate(m, aud, openapi.Info{Title: "Users"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// roadmap.md M1：min=partner のフィールドと internal だけの操作が openapi/public に出ない。
func TestPublicDocumentHidesNarrowerThings(t *testing.T) {
	m := load(t)
	pub := gen(t, m, "public")
	paths := pub["paths"].(map[string]any)
	if len(paths) != 1 || paths["/users/{id}"] == nil {
		t.Fatalf("public paths: %v", keys(paths))
	}
	get := paths["/users/{id}"].(map[string]any)["get"].(map[string]any)
	props := get["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)
	if props["email"] != nil || props["note"] != nil || props["name"] == nil {
		t.Errorf("public User properties: %v", keys(props))
	}

	internal := gen(t, m, "internal")
	ipaths := internal["paths"].(map[string]any)
	users := ipaths["/users"].(map[string]any)
	if users["get"] == nil || users["post"] == nil {
		t.Errorf("internal /users: %v", keys(users))
	}
	if ipaths["/users/{id}"].(map[string]any)["delete"].(map[string]any)["x-idempotent"] != true {
		t.Error("DELETE should be marked idempotent")
	}
}

func TestDeterministic(t *testing.T) {
	m := load(t)
	a, _ := openapi.Generate(m, "partner", openapi.Info{})
	b, _ := openapi.Generate(m, "partner", openapi.Info{})
	if !bytes.Equal(a, b) {
		t.Fatal("output differs between runs")
	}
	if !strings.Contains(string(a), `"openapi": "3.1.0"`) {
		t.Fatal("not OpenAPI 3.1")
	}
}

// roadmap.md M1：マニフェストを書き出して読み直すと同じ API になる。
func TestManifestRoundTrip(t *testing.T) {
	m := load(t)
	b, err := manifest.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := manifest.Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := manifest.Marshal(m2)
	if !bytes.Equal(b, b2) {
		t.Fatal("manifest changed after a round trip")
	}
	for _, aud := range m.AudienceList() {
		x, _ := openapi.Generate(m, aud, openapi.Info{})
		y, _ := openapi.Generate(m2, aud, openapi.Info{})
		if !bytes.Equal(x, y) {
			t.Errorf("%s: OpenAPI differs after a round trip", aud)
		}
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
