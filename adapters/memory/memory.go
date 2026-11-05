// Package memory はテストと開発のための、インメモリの認証・認可の実装（ADR 0004）。
package memory

import (
	"context"
	"sync"

	"github.com/imohiyoko/API/port"
)

// Authenticator はトークンから主体を引く。
type Authenticator struct {
	mu     sync.RWMutex
	tokens map[string]port.Principal
}

// NewAuthenticator は空の Authenticator を作る。
func NewAuthenticator() *Authenticator {
	return &Authenticator{tokens: map[string]port.Principal{}}
}

// Add はトークンと主体を足す。
func (a *Authenticator) Add(token string, p port.Principal) *Authenticator {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tokens[token] = p
	return a
}

// Authenticate は port.Authenticator の実装。
func (a *Authenticator) Authenticate(_ context.Context, token string) (port.Principal, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	p, ok := a.tokens[token]
	if !ok {
		return port.Principal{}, port.ErrUnauthenticated
	}
	p.Token = token
	return p, nil
}

// Grant は許可の1件。ResourceID が空ならその種類のすべてのリソース。
type Grant struct {
	PrincipalID  string
	Action       string
	ResourceType string
	ResourceID   string
}

// Authorizer は Grant の一覧で判定する。
type Authorizer struct {
	mu     sync.RWMutex
	grants []Grant
}

// NewAuthorizer は Grant を持つ Authorizer を作る。
func NewAuthorizer(grants ...Grant) *Authorizer {
	return &Authorizer{grants: grants}
}

// Allow は Grant を足す。
func (a *Authorizer) Allow(g Grant) *Authorizer {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.grants = append(a.grants, g)
	return a
}

// Check は port.Authorizer の実装。
func (a *Authorizer) Check(_ context.Context, req port.CheckRequest) (port.Decision, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, g := range a.grants {
		if g.PrincipalID != req.Principal.ID || g.Action != req.Action {
			continue
		}
		if g.ResourceType != "" && g.ResourceType != req.ResourceType {
			continue
		}
		if g.ResourceID != "" && g.ResourceID != req.ResourceID {
			continue
		}
		return port.Decision{Allowed: true}, nil
	}
	return port.Decision{Allowed: false, Reasons: []string{"no_grant"}}, nil
}

// DevTokens は開発用のトークン（例のサーバーと `api sidecar -dev` が使う）。本番では使わない。
//
//	dev-internal：internal 用のサービスアカウント
//	dev-partner：提携先 org_partner のサービスアカウント
//	dev-user：利用者
func DevTokens(actions ...string) (*Authenticator, *Authorizer) {
	authn := NewAuthenticator().
		Add("dev-internal", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_internal"}).
		Add("dev-partner", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_partner", OrgID: "org_partner"}).
		Add("dev-user", port.Principal{Type: port.PrincipalUser, ID: "usr_dev"})
	authz := NewAuthorizer()
	for _, id := range []string{"sa_internal", "sa_partner", "usr_dev"} {
		for _, a := range actions {
			authz.Allow(Grant{PrincipalID: id, Action: a})
		}
	}
	return authn, authz
}
