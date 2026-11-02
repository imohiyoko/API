package define

import (
	"errors"
	"fmt"

	"github.com/imohiyoko/API/manifest"
)

// Registry は操作を集める。
type Registry struct {
	name      string
	audiences []string
	ops       []Operation
}

// RegistryOption は Registry の設定。
type RegistryOption func(*Registry)

// WithAudiences は公開範囲を広い順に置き換える（独自の公開範囲。design.md 4.1）。
func WithAudiences(audiences ...string) RegistryOption {
	return func(r *Registry) { r.audiences = audiences }
}

// WithName は API の名前（manifest.Manifest.Name）。gRPC のパッケージ名の頭になる。
func WithName(name string) RegistryOption {
	return func(r *Registry) { r.name = name }
}

// NewRegistry は空の Registry を作る。
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Add は操作を足す。
func (r *Registry) Add(ops ...Operation) *Registry {
	r.ops = append(r.ops, ops...)
	return r
}

// Operations は足した順の操作。
func (r *Registry) Operations() []Operation { return r.ops }

// Manifest はマニフェストを書き出す。定義の誤り（型の対応が無い、ハンドラーが無いなど）は error。
// 公開の規則の検査は manifest.Check で行う。
func (r *Registry) Manifest() (*manifest.Manifest, error) {
	m := &manifest.Manifest{Version: manifest.Version, Name: r.name, Audiences: r.audiences, Operations: []manifest.Operation{}}
	var errs []error
	for _, op := range r.ops {
		spec, err := op.Spec()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		m.Operations = append(m.Operations, spec)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("define: %w", errors.Join(errs...))
	}
	return m, nil
}
