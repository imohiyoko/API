// Package iam は imohiyoko/iam を使う認証・認可の実装（design.md 6章、ADR 0004）。
//
// 認証はイントロスペクション（POST /oauth2/introspect、RFC 7662）、認可は管理 API の POST /v1/admin/authz:check。
// この版で確かめられるのは、iam が OIDC の連携先に発行したユーザーのアクセストークンだけ。
// サービスアカウントのトークンと API キーは imohiyoko/iam#151、サービスアカウントの判定は #152 を待つ。
package iam

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/imohiyoko/API/port"
)

// Config は iam への接続の設定。
type Config struct {
	// PublicURL は iam の公開 API の URL（イントロスペクションを呼ぶ）。例：https://iam.example.com
	PublicURL string
	// AdminURL は iam の管理 API の URL（authz:check を呼ぶ）。例：https://iam-admin.internal:8081
	AdminURL string
	// ClientID・ClientSecret は、イントロスペクションを呼ぶ機密の連携先の認証情報（client_secret_basic）。
	ClientID     string
	ClientSecret string
	// AdminToken は、権限 authz:check を持つサービスアカウントの API キーかアクセストークン。
	AdminToken string
	// CacheTTL はイントロスペクションの結果を持つ時間。iam の session.validity_max_delay（既定30秒）以下にする。0 なら30秒。
	CacheTTL   time.Duration
	HTTPClient *http.Client
	// Now は時刻（テスト用）。nil なら time.Now。
	Now func() time.Time
}

// maxCache はイントロスペクションの結果を持つ件数の上限。超えたら期限切れを捨て、それでも多ければ持たない。
const maxCache = 10000

// Client は port.Authenticator と port.Authorizer を実装する。
type Client struct {
	cfg   Config
	mu    sync.Mutex
	cache map[[32]byte]cached
}

type cached struct {
	p       port.Principal
	active  bool
	expires time.Time
}

// New は Client を作る。
func New(cfg Config) (*Client, error) {
	if cfg.PublicURL == "" || cfg.AdminURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.AdminToken == "" {
		return nil, errors.New("iam: PublicURL, AdminURL, ClientID, ClientSecret and AdminToken are required")
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 30 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{cfg: cfg, cache: map[[32]byte]cached{}}, nil
}

// introspection は RFC 7662 の応答のうち使うもの。
type introspection struct {
	Active    bool   `json:"active"`
	Sub       string `json:"sub"`
	OrgID     string `json:"org_id"`
	SID       string `json:"sid"`
	TokenType string `json:"token_type"`
	Exp       int64  `json:"exp"`
}

// Authenticate は port.Authenticator の実装。
func (c *Client) Authenticate(ctx context.Context, token string) (port.Principal, error) {
	key := sha256.Sum256([]byte(token))
	now := c.cfg.Now()
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		if !e.active {
			return port.Principal{}, port.ErrUnauthenticated
		}
		return e.p, nil
	}
	c.mu.Unlock()

	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.PublicURL, "/")+"/oauth2/introspect", strings.NewReader(form.Encode()))
	if err != nil {
		return port.Principal{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(c.cfg.ClientID), url.QueryEscape(c.cfg.ClientSecret))
	var res introspection
	if err := c.do(req, &res); err != nil {
		return port.Principal{}, fmt.Errorf("iam: introspect: %w", err)
	}
	// アクセストークンだけを受ける（リフレッシュトークンは API の認証情報ではない）。
	active := res.Active && res.Sub != "" && (res.TokenType == "" || res.TokenType == "access_token")
	p := port.Principal{Type: port.PrincipalUser, ID: res.Sub, OrgID: res.OrgID, SID: res.SID, Token: token}
	expires := now.Add(c.cfg.CacheTTL)
	if active && res.Exp > 0 {
		if exp := time.Unix(res.Exp, 0); exp.Before(expires) {
			expires = exp
		}
	}
	c.mu.Lock()
	if len(c.cache) >= maxCache {
		for k, e := range c.cache {
			if !now.Before(e.expires) {
				delete(c.cache, k)
			}
		}
	}
	if len(c.cache) < maxCache {
		c.cache[key] = cached{p: p, active: active, expires: expires}
	}
	c.mu.Unlock()
	if !active {
		return port.Principal{}, port.ErrUnauthenticated
	}
	return p, nil
}

type checkRequest struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	Action        string `json:"action"`
	OrgID         string `json:"org_id,omitempty"`
	ResourceType  string `json:"resource_type,omitempty"`
	ResourceID    string `json:"resource_id,omitempty"`
	AccessToken   string `json:"access_token,omitempty"`
}

type checkResult struct {
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons"`
	Unmet   []string `json:"unmet"`
}

// Check は port.Authorizer の実装。主体のアクセストークンを渡し、認証の文脈は iam に組み立てさせる（attested）。
func (c *Client) Check(ctx context.Context, r port.CheckRequest) (port.Decision, error) {
	body, err := json.Marshal(checkRequest{
		PrincipalType: r.Principal.Type,
		PrincipalID:   r.Principal.ID,
		Action:        r.Action,
		OrgID:         r.Principal.OrgID,
		ResourceType:  r.ResourceType,
		ResourceID:    r.ResourceID,
		AccessToken:   r.Principal.Token,
	})
	if err != nil {
		return port.Decision{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.AdminURL, "/")+"/v1/admin/authz:check", bytes.NewReader(body))
	if err != nil {
		return port.Decision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.AdminToken)
	var res checkResult
	if err := c.do(req, &res); err != nil {
		var se *statusError
		// 主体か Org が無ければ 404（iam の仕様）。拒否として扱う。
		if errors.As(err, &se) && se.status == http.StatusNotFound {
			return port.Decision{Allowed: false, Reasons: []string{"not_found"}}, nil
		}
		return port.Decision{}, fmt.Errorf("iam: authz:check: %w", err)
	}
	return port.Decision{Allowed: res.Allowed, Reasons: append(res.Reasons, res.Unmet...)}, nil
}

type statusError struct {
	status int
	body   string
}

func (e *statusError) Error() string { return fmt.Sprintf("status %d: %s", e.status, e.body) }

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &statusError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
	}
	return json.Unmarshal(b, out)
}
