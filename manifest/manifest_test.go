package manifest_test

import (
	"strings"
	"testing"

	"github.com/imohiyoko/API/manifest"
)

func TestReadYAML(t *testing.T) {
	m, err := manifest.ReadFile("m.yaml", strings.NewReader(`
version: api.manifest/v0
name: things
operations:
- name: things.get
  kind: query
  anonymous: true
  expose: [{audience: public, protocols: [rest]}]
  input: {type: object, fields: [{name: id, in: path, schema: {type: string}}]}
  output: {type: string}
`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "things" || len(m.Operations) != 1 || m.Operations[0].Input.Fields[0].In != "path" {
		t.Fatalf("got %+v", m)
	}
	for name, src := range map[string]string{
		"unknown key":     "version: api.manifest/v0\noperations: []\nextra: 1\n",
		"wrong version":   "version: v9\noperations: []\n",
		"two documents":   "version: api.manifest/v0\noperations: []\n---\nversion: api.manifest/v0\n",
		"non-string keys": "version: api.manifest/v0\noperations: []\n1: x\n",
		"not yaml":        "version: [\n",
	} {
		if _, err := manifest.ReadYAML(strings.NewReader(src)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
