// Package define は、操作を Go で1回だけ定義するための DSL を置く（design.md 3章、ADR 0001・0002）。
//
//	var GetUser = define.Query[GetUserIn, User]("users.get",
//		define.Authz("users:read", define.Resource("user", "id")),
//		define.Expose(define.Public, define.REST),
//	).Handle(getUser)
//
// 定義は Registry に集め、Manifest で言語に依存しないマニフェストに書き出す。
package define

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/imohiyoko/API/manifest"
)

// 既定の公開範囲（design.md 4.1）。
const (
	Internal = manifest.Internal
	Partner  = manifest.Partner
	Public   = manifest.Public
)

// プロトコル（design.md 5章）。この版で実行できるのは REST だけ（roadmap.md M1）。
const (
	REST      = manifest.REST
	GRPC      = manifest.GRPC
	GraphQL   = manifest.GraphQL
	SOAP      = manifest.SOAP
	WebSocket = manifest.WebSocket
	Webhook   = manifest.Webhook
)

// Operation は定義した操作。runtime はこのインターフェースだけを使う。
type Operation interface {
	// Spec は操作のマニフェストを返す。定義に誤りがあれば error。
	Spec() (manifest.Operation, error)
	// Decode は要求から入力を組み立てる。返す値は Call と ResourceID に渡す。
	Decode(src InputSource) (any, error)
	// ResourceID は入力のフィールド（API 上の名前）の値を文字列で返す。
	ResourceID(in any, field string) string
	// Call はハンドラーを呼ぶ。
	Call(ctx context.Context, in any) (any, error)
}

// Option は操作の定義に足すもの。
type Option func(*manifest.Operation)

// Doc は操作の説明。
func Doc(text string) Option {
	return func(o *manifest.Operation) { o.Doc = text }
}

// ResourceOption は Authz の対象のリソース。
type ResourceOption struct{ Type, Field string }

// Resource は、リソースの種類と、ID を取る入力のフィールド（API 上の名前）。
func Resource(typ, field string) ResourceOption {
	return ResourceOption{Type: typ, Field: field}
}

// Authz は認可の宣言。要求ごとに iam の authz:check で判定する（design.md 6章）。
func Authz(action string, resource ...ResourceOption) Option {
	return func(o *manifest.Operation) {
		a := &manifest.Authz{Action: action}
		if len(resource) > 0 {
			a.ResourceType = resource[0].Type
			a.ResourceField = resource[0].Field
		}
		o.Authz = a
	}
}

// Anonymous は、認証なしで呼べる操作にする。どの公開範囲に出す操作にも Authz か Anonymous が要る（design.md 4.2）。
func Anonymous() Option {
	return func(o *manifest.Operation) { o.Anonymous = true }
}

// RateLimit は公開範囲ごとのレートリミット（例：RateLimit(Partner, "600/min")）。単位は s・min・h。
// 呼び出し元ごとに数える：partner は Org ごと、それ以外は主体ごと、匿名の呼び出しは接続元のアドレスごと（design.md 4.4）。
func RateLimit(audience, limit string) Option {
	return func(o *manifest.Operation) {
		o.RateLimits = append(o.RateLimits, manifest.RateLimit{Audience: audience, Limit: limit})
	}
}

// Expose は公開の許可。公開範囲に、どのプロトコルで出すか。書かない操作はどこにも出ない。
func Expose(audience string, protocols ...string) Option {
	return func(o *manifest.Operation) {
		for i := range o.Expose {
			if o.Expose[i].Audience == audience {
				o.Expose[i].Protocols = append(o.Expose[i].Protocols, protocols...)
				return
			}
		}
		o.Expose = append(o.Expose, manifest.Exposure{Audience: audience, Protocols: protocols})
	}
}

// HTTP は REST のメソッドとパスの上書き。"GET /v1/users/{id}" か、パスだけ（"/v1/users/{id}"）。
func HTTP(route string) Option {
	return func(o *manifest.Operation) {
		h := &manifest.HTTP{}
		if method, path, ok := strings.Cut(route, " "); ok {
			h.Method, h.Path = strings.ToUpper(method), strings.TrimSpace(path)
		} else {
			h.Path = route
		}
		o.HTTP = h
	}
}

// Op は型付きの操作。
type Op[In, Out any] struct {
	spec    manifest.Operation
	err     error
	in      *structPlan
	handler func(context.Context, In) (Out, error)
}

// Query は安全な読み取りの操作を定義する。入力のフィールドの既定の場所は query。
func Query[In, Out any](name string, opts ...Option) *Op[In, Out] {
	return newOp[In, Out](name, manifest.KindQuery, "query", opts)
}

// Command は更新の操作を定義する。入力のフィールドの既定の場所は body。
func Command[In, Out any](name string, opts ...Option) *Op[In, Out] {
	return newOp[In, Out](name, manifest.KindCommand, "body", opts)
}

func newOp[In, Out any](name, kind, defaultIn string, opts []Option) *Op[In, Out] {
	o := &Op[In, Out]{spec: manifest.Operation{Name: name, Kind: kind}}
	for _, opt := range opts {
		opt(&o.spec)
	}
	inType := reflect.TypeFor[In]()
	plan, err := planFor(inType)
	if err != nil {
		o.err = fmt.Errorf("input: %w", err)
		return o
	}
	if plan == nil {
		o.err = fmt.Errorf("input must be a struct, not %s", inType)
		return o
	}
	o.in = plan
	o.spec.Input, err = inputSchema(plan, defaultIn)
	if err != nil {
		o.err = fmt.Errorf("input: %w", err)
		return o
	}
	o.spec.Output, err = schemaFor(reflect.TypeFor[Out]())
	if err != nil {
		o.err = fmt.Errorf("output: %w", err)
	}
	return o
}

// Handle はハンドラーを付ける。業務ロジックはここだけに書く。
func (o *Op[In, Out]) Handle(fn func(context.Context, In) (Out, error)) *Op[In, Out] {
	o.handler = fn
	return o
}

// Name は操作の名前。
func (o *Op[In, Out]) Name() string { return o.spec.Name }

// Spec は Operation の実装。
func (o *Op[In, Out]) Spec() (manifest.Operation, error) {
	if o.err != nil {
		return manifest.Operation{}, fmt.Errorf("%s: %w", o.spec.Name, o.err)
	}
	if o.handler == nil {
		return manifest.Operation{}, fmt.Errorf("%s: no handler (call Handle)", o.spec.Name)
	}
	return o.spec, nil
}

// Decode は Operation の実装。
func (o *Op[In, Out]) Decode(src InputSource) (any, error) {
	var in In
	if err := decodeInput(reflect.ValueOf(&in).Elem(), o.in, o.spec.Input, src); err != nil {
		return nil, err
	}
	return in, nil
}

// ResourceID は Operation の実装。
func (o *Op[In, Out]) ResourceID(in any, field string) string {
	v := reflect.ValueOf(in)
	for _, f := range o.in.fields {
		if f.name == field {
			return scalarString(v.FieldByIndex(f.index))
		}
	}
	return ""
}

// Call は Operation の実装。
func (o *Op[In, Out]) Call(ctx context.Context, in any) (any, error) {
	return o.handler(ctx, in.(In))
}
