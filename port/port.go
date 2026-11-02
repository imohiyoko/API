// Package port は外部依存（認証・認可）のポートを置く（ADR 0004）。実装は adapters/ に置き、コアは adapters を import しない。
package port

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// 主体の種類。iam の principal_type と同じ。
const (
	PrincipalUser           = "user"
	PrincipalServiceAccount = "service_account"
)

// Principal は認証済みの呼び出し元。
type Principal struct {
	Type string
	// ID は主体の公開 ID（iam の sub）。
	ID string
	// OrgID は主体の Org（iam の org_id）。無ければ空。
	OrgID string
	// SID はログインの系列（iam の sid）。無ければ空。
	SID string
	// Token は認証に使ったトークン。認可で iam に認証の文脈を組み立てさせるために渡す（design.md 6章）。ログに出さない。
	Token string `json:"-"`
}

// String は Token を伏せた表現を返す（%v や %+v でトークンを出さない）。
func (p Principal) String() string {
	return fmt.Sprintf("{Type:%s ID:%s OrgID:%s SID:%s}", p.Type, p.ID, p.OrgID, p.SID)
}

// LogValue は slog に Token を出さない。
func (p Principal) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", p.Type), slog.String("id", p.ID),
		slog.String("org_id", p.OrgID), slog.String("sid", p.SID),
	)
}

// ErrUnauthenticated は、認証情報が無い・使えないことを表す。
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticator はトークン（Bearer）を確かめる。使えなければ ErrUnauthenticated。
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
}

// CheckRequest は認可の判定の要求（iam の check(principal, action, resource, context)）。
type CheckRequest struct {
	Principal    Principal
	Action       string
	ResourceType string
	ResourceID   string
}

// Decision は判定の結果。
type Decision struct {
	Allowed bool
	// Reasons は拒否の理由のコード。
	Reasons []string
}

// Authorizer は認可を判定する。
type Authorizer interface {
	Check(ctx context.Context, req CheckRequest) (Decision, error)
}

// RateLimiter はレートリミットを数える。既定は runtime のプロセス内の実装。複数のプロセスで数えるなら、共有の保存先の実装を渡す。
type RateLimiter interface {
	// Allow は key の呼び出しを1回数え、許すか、許さないならもう一度呼べるまでの時間を返す。
	Allow(ctx context.Context, key string, limit Rate) (allowed bool, retryAfter time.Duration, err error)
}

// Rate は期間あたりの回数。
type Rate struct {
	Requests int
	Per      time.Duration
}
