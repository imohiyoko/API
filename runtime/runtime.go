// Package runtime は、定義から公開範囲ごとの口を組み立てる共通の層（design.md 2.2・4章・6章）。
// 入力の検証・認証・認可・エラーの開示は、プロトコルに入る前にここで行う。
package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/manifest"
	"github.com/imohiyoko/API/port"
)

// Disclosure はエラーをどこまで見せるか（design.md 4.1）。
type Disclosure int

const (
	// DiscloseCode はエラーのコードだけを見せる（public の既定）。
	DiscloseCode Disclosure = iota
	// DiscloseReasons は拒否の理由のコードも見せる（partner の既定）。
	DiscloseReasons
	// DiscloseDetail はメッセージも見せる（internal の既定）。
	DiscloseDetail
)

// AudienceConfig は公開範囲ごとの設定。
type AudienceConfig struct {
	// Principals はこの口で受ける主体の種類（port.PrincipalUser など）。空なら既定。
	Principals []string
	// RequireOrg は、Org に属する主体だけを受けるか（partner の既定）。
	RequireOrg bool
	// Resource は、この口が受けるトークンの対象（RFC 8707 の resource、iam に登録したリソースの URI）。
	// 空でなければ、Principal.Audiences にこれを含むトークンだけを受ける（ほかの API や口に向けたトークンを拒む）。
	Resource   string
	Disclosure Disclosure
	// GRPCReflection は、gRPC の口（runtime/grpcx）にサーバーリフレクションを載せるか（internal の既定）。
	// 載せると、契約（.proto）を持たない呼び出し元にも、その公開範囲の操作と型が見える。
	GRPCReflection bool
}

// DefaultAudienceConfig は既定の公開範囲の既定の設定。独自の公開範囲は、すべての主体を受け、コードだけを見せる。
func DefaultAudienceConfig(audience string) AudienceConfig {
	switch audience {
	case manifest.Internal:
		return AudienceConfig{Principals: []string{port.PrincipalServiceAccount}, Disclosure: DiscloseDetail, GRPCReflection: true}
	case manifest.Partner:
		return AudienceConfig{Principals: []string{port.PrincipalServiceAccount}, RequireOrg: true, Disclosure: DiscloseReasons}
	case manifest.Public:
		return AudienceConfig{Principals: []string{port.PrincipalUser, port.PrincipalServiceAccount}, Disclosure: DiscloseCode}
	}
	return AudienceConfig{Principals: []string{port.PrincipalUser, port.PrincipalServiceAccount}, Disclosure: DiscloseCode}
}

// Config は Build の設定。
type Config struct {
	Authenticator port.Authenticator
	Authorizer    port.Authorizer
	// RateLimiter はレートリミットを数える。nil ならプロセス内で数える（NewMemoryRateLimiter）。
	RateLimiter port.RateLimiter
	// Audiences は公開範囲ごとの設定の上書き。
	Audiences map[string]AudienceConfig
	Logger    *slog.Logger
}

// Server は公開範囲ごとの口を持つ。
// REST の口（Handler）とプロセス内の口（Invoke）はこのパッケージに、gRPC の口は runtime/grpcx にある。
// gRPC を使わない利用者のビルドに grpc・protobuf が入らないよう分けている（design.md 2.3）。
type Server struct {
	manifest  *manifest.Manifest
	warnings  []manifest.Diagnostic
	handlers  map[string]http.Handler
	calls     map[string]map[string]*opCall // 公開範囲 → 操作の名前 → 呼び出し（その公開範囲に出す操作だけ）
	audiences map[string]AudienceConfig
	logger    *slog.Logger
}

// Build は定義を検査し（design.md 4.3）、公開範囲ごとの口を組み立てる。エラーがあれば起動を拒む。
func Build(reg *define.Registry, cfg Config) (*Server, error) {
	m, err := reg.Manifest()
	if err != nil {
		return nil, err
	}
	ds := manifest.Check(m)
	if manifest.HasErrors(ds) {
		return nil, &CheckError{Diagnostics: ds}
	}
	if cfg.Authenticator == nil || cfg.Authorizer == nil {
		return nil, errors.New("runtime: Authenticator and Authorizer are required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RateLimiter == nil {
		cfg.RateLimiter = NewMemoryRateLimiter(nil)
	}
	s := &Server{manifest: m, handlers: map[string]http.Handler{}, calls: map[string]map[string]*opCall{}, audiences: map[string]AudienceConfig{}, logger: cfg.Logger}
	for _, d := range ds {
		s.warnings = append(s.warnings, d)
		cfg.Logger.Warn("api definition", "diagnostic", d.String())
	}
	ops := map[string]define.Operation{}
	for _, op := range reg.Operations() {
		spec, _ := op.Spec()
		ops[spec.Name] = op
	}
	for _, aud := range m.AudienceList() {
		ac, ok := cfg.Audiences[aud]
		if !ok {
			ac = DefaultAudienceConfig(aud)
		}
		if len(ac.Principals) == 0 {
			ac.Principals = DefaultAudienceConfig(aud).Principals
		}
		s.audiences[aud] = ac
		calls := map[string]*opCall{}
		for i := range m.Operations {
			spec := &m.Operations[i]
			if spec.ExposedTo(aud) {
				calls[spec.Name] = &opCall{m: m, aud: aud, ac: ac, spec: spec, op: ops[spec.Name], cfg: cfg}
			}
		}
		s.calls[aud] = calls
		s.handlers[aud] = newRESTHandler(m, aud, calls)
	}
	return s, nil
}

// Handler は公開範囲の REST の口。知らない公開範囲なら、すべて 404 を返す。
// 公開範囲ごとに別の listener で出す（design.md 4.1、ADR 0005）。
func (s *Server) Handler(audience string) http.Handler {
	if h, ok := s.handlers[audience]; ok {
		return h
	}
	return http.NotFoundHandler()
}

// AudienceConfig は公開範囲の設定（上書きと既定を合わせたもの）。知らない公開範囲なら false。
func (s *Server) AudienceConfig(audience string) (AudienceConfig, bool) {
	ac, ok := s.audiences[audience]
	return ac, ok
}

// Logger は Config.Logger（無ければ slog.Default()）。
func (s *Server) Logger() *slog.Logger { return s.logger }

// Manifest は組み立てに使ったマニフェスト。
func (s *Server) Manifest() *manifest.Manifest { return s.manifest }

// Warnings は検査の警告。
func (s *Server) Warnings() []manifest.Diagnostic { return s.warnings }

// CheckError は定義の検査のエラー。
type CheckError struct {
	Diagnostics []manifest.Diagnostic
}

func (e *CheckError) Error() string {
	msg := "runtime: invalid API definition:"
	for _, d := range e.Diagnostics {
		if d.Severity == manifest.Error {
			msg += fmt.Sprintf("\n  %s", d)
		}
	}
	return msg
}
