// Package manifest は、言語に依存しない API の定義（マニフェスト）の型と読み書きを置く（design.md 2.2、ADR 0002）。
// 契約ファイルの生成と実行時の判断は、このマニフェストだけを見る。
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Version はマニフェストの形式の版。互換性のルールは未決（design.md 7章）。
const Version = "api.manifest/v0"

// 既定の公開範囲（design.md 4.1）。
const (
	Internal = "internal"
	Partner  = "partner"
	Public   = "public"
)

// DefaultAudiences は既定の公開範囲を、広い順に並べたもの。
var DefaultAudiences = []string{Internal, Partner, Public}

// 操作の種類（design.md 2.1）。
const (
	KindQuery   = "query"
	KindCommand = "command"
	KindStream  = "stream"
	KindEvent   = "event"
)

// プロトコル（design.md 5章）。
const (
	REST      = "rest"
	GRPC      = "grpc"
	GraphQL   = "graphql"
	SOAP      = "soap"
	WebSocket = "websocket"
	Webhook   = "webhook"
)

// Protocols は知っているプロトコルの一覧。
var Protocols = []string{REST, GRPC, GraphQL, SOAP, WebSocket, Webhook}

// Implemented はこの版で実行と生成ができるプロトコル（roadmap.md M1）。
var Implemented = map[string]bool{REST: true}

// Manifest は API 全体の定義。
type Manifest struct {
	Version string `json:"version"`
	// Audiences は公開範囲を広い順に並べたもの。空なら DefaultAudiences。
	Audiences  []string    `json:"audiences,omitempty"`
	Operations []Operation `json:"operations"`
}

// Operation は1つの操作。
type Operation struct {
	// Name は `users.get` のような、小文字の語をドットでつないだ名前。
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Doc       string     `json:"doc,omitempty"`
	Authz     *Authz     `json:"authz,omitempty"`
	Anonymous bool       `json:"anonymous,omitempty"`
	Expose    []Exposure `json:"expose,omitempty"`
	HTTP      *HTTP      `json:"http,omitempty"`
	Input     Schema     `json:"input"`
	Output    Schema     `json:"output"`
}

// Authz は認可の宣言。iam の `authz:check` に渡す（design.md 6章）。
type Authz struct {
	Action       string `json:"action"`
	ResourceType string `json:"resource_type,omitempty"`
	// ResourceField はリソースの ID を取る入力のフィールドの名前（API 上の名前）。
	ResourceField string `json:"resource_field,omitempty"`
}

// Exposure は公開の許可。1つの公開範囲に、どのプロトコルで出すか。
type Exposure struct {
	Audience  string   `json:"audience"`
	Protocols []string `json:"protocols"`
}

// HTTP は REST の上書き。空の値は既定の規則で決める（design.md 5.1）。
type HTTP struct {
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
}

// Schema は入力・出力の型。
type Schema struct {
	// Type は object・string・integer・number・boolean・array のどれか。
	Type   string  `json:"type"`
	Format string  `json:"format,omitempty"`
	Items  *Schema `json:"items,omitempty"`
	Fields []Field `json:"fields,omitempty"`
}

// Field は object のフィールド。
type Field struct {
	Name string `json:"name"`
	// In は入力のときの場所（path・query・body）。出力では空。
	In string `json:"in,omitempty"`
	// Min はこのフィールドを見せる最も狭い公開範囲。空なら操作を出すすべての公開範囲に出る（design.md 4.2）。
	Min      string `json:"min,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Schema   Schema `json:"schema"`
}

// AudienceList は公開範囲を広い順に返す。
func (m *Manifest) AudienceList() []string {
	if len(m.Audiences) == 0 {
		return DefaultAudiences
	}
	return m.Audiences
}

// Rank は公開範囲の広さの順位（0 が最も広い）。知らない公開範囲なら -1。
func (m *Manifest) Rank(audience string) int {
	for i, a := range m.AudienceList() {
		if a == audience {
			return i
		}
	}
	return -1
}

// Visible は、min を付けたフィールドが audience に見えるか。
func (m *Manifest) Visible(min, audience string) bool {
	if min == "" {
		return true
	}
	r, a := m.Rank(min), m.Rank(audience)
	return r >= 0 && a >= 0 && a <= r
}

// Exposed は操作が audience に protocol で出るか。
func (o *Operation) Exposed(audience, protocol string) bool {
	for _, e := range o.Expose {
		if e.Audience != audience {
			continue
		}
		for _, p := range e.Protocols {
			if p == protocol {
				return true
			}
		}
	}
	return false
}

// Operation は名前で操作を探す。
func (m *Manifest) Operation(name string) (*Operation, bool) {
	for i := range m.Operations {
		if m.Operations[i].Name == name {
			return &m.Operations[i], true
		}
	}
	return nil, false
}

// Marshal はマニフェストを JSON にする。
func Marshal(m *Manifest) ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Read は JSON のマニフェストを読む。知らないキーは誤りにする。
func Read(r io.Reader) (*Manifest, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Version != Version {
		return nil, fmt.Errorf("manifest: unsupported version %q (want %q)", m.Version, Version)
	}
	return &m, nil
}

// Unmarshal は Read のバイト列版。
func Unmarshal(b []byte) (*Manifest, error) {
	return Read(bytes.NewReader(b))
}
