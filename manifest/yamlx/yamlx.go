// Package yamlx は、YAML で書いたマニフェストを読む（Go 以外の本体が直接書く経路。roadmap.md M3）。
// YAML を使う利用者だけがこのパッケージを import し、YAML のライブラリをビルドに含める（design.md 2.3）。
// JSON のマニフェストは manifest.Read で読める。
package yamlx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/imohiyoko/API/manifest"
)

// Read は YAML で書いたマニフェストを読む。
// キーは JSON のマニフェストと同じで、知らないキーは誤りにする。
func Read(r io.Reader) (*manifest.Manifest, error) {
	dec := yaml.NewDecoder(r)
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("yamlx: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("yamlx: want a single document")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("yamlx: %w", err)
	}
	return manifest.Read(bytes.NewReader(b))
}

// ReadFile は拡張子（.yaml・.yml なら YAML、それ以外は JSON）で形式を選んで読む。
func ReadFile(path string, r io.Reader) (*manifest.Manifest, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return Read(r)
	}
	return manifest.Read(r)
}
