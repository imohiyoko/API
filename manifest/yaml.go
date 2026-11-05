package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ReadYAML は YAML で書いたマニフェストを読む（Go 以外の本体が直接書く経路。roadmap.md M3）。
// キーは JSON のマニフェストと同じで、知らないキーは誤りにする。
func ReadYAML(r io.Reader) (*Manifest, error) {
	dec := yaml.NewDecoder(r)
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("manifest: yaml: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("manifest: yaml: want a single document")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("manifest: yaml: %w", err)
	}
	return Read(bytes.NewReader(b))
}

// ReadFile は拡張子（.yaml・.yml なら YAML、それ以外は JSON）で形式を選んで読む。
func ReadFile(path string, r io.Reader) (*Manifest, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return ReadYAML(r)
	}
	return Read(r)
}
