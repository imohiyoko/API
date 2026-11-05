// Package iam は imohiyoko/iam を使う認証・認可の実装（design.md 6章、ADR 0004）。
//
// 認証はイントロスペクション（POST /oauth2/introspect、RFC 7662）。受けるのは次の2つ。
//   - iam が OIDC の連携先に発行したユーザーのアクセストークン（atk_…）
//   - サービスアカウントが client credentials（POST /v1/admin/token）で、iam に登録したリソースに向けて取ったトークン
//     （sat_…、iam の ADR 0112）。管理 API 向けのトークンと API キー（sac_…）は iam が active: false を返すので受けない
//
// DPoP・mTLS に結び付けたトークン（cnf のあるもの）は、結び付けを確かめられないので受けない。
//
// 認可は、ユーザーなら管理 API の POST /v1/admin/authz:check。サービスアカウントは iam の check が主体として受けない
// （imohiyoko/iam#152）ので、それまではトークンのスコープに操作の Authz（例：users:read）があるかで判定する。
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
	"slices"
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

// serviceAccountPrefix は iam のサービスアカウントの ID の接頭辞。
const serviceAccountPrefix = "sva_"

// introspection は RFC 7662 の応答のうち使うもの。
type introspection struct {
	Active    bool            `json:"active"`
	Sub       string          `json:"sub"`
	ClientID  string          `json:"client_id"`
	OrgID     string          `json:"org_id"`
	SID       string          `json:"sid"`
	TokenType string          `json:"token_type"`
	Exp       int64           `json:"exp"`
	Scope     string          `json:"scope"`
	Aud       audience        `json:"aud"`
	Cnf       json.RawMessage `json:"cnf"`
}

// audience は aud（RFC 7662 では文字列か文字列の配列）。
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// principal は応答を主体にする。受けないトークンなら ok が偽。
func (r introspection) principal(token string) (p port.Principal, ok bool) {
	// アクセストークンだけを受ける（リフレッシュトークン・Personal Access Token は API の認証情報にしない）。
	if !r.Active || r.Sub == "" || (r.TokenType != "" && r.TokenType != "access_token") {
		return p, false
	}
	// 結び付けたトークンは、同じ鍵の証明（DPoP）か証明書を確かめないと盗んだ者にも使える。確かめられないので拒む。
	if len(r.Cnf) > 0 && string(r.Cnf) != "null" {
		return p, false
	}
	p = port.Principal{Type: port.PrincipalUser, ID: r.Sub, OrgID: r.OrgID, SID: r.SID, Token: token,
		Audiences: []string(r.Aud), Scopes: strings.Fields(r.Scope)}
	if strings.HasPrefix(r.Sub, serviceAccountPrefix) {
		// サービスアカウントのトークンは client_id と sub がどちらもサービスアカウントの ID。
		if r.ClientID != r.Sub {
			return port.Principal{}, false
		}
		p.Type = port.PrincipalServiceAccount
		p.SID = ""
	}
	return p, true
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
	p, active := res.principal(token)
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
// サービスアカウントは、iam の check が受けるまで（imohiyoko/iam#152）トークンのスコープで判定する。
func (c *Client) Check(ctx context.Context, r port.CheckRequest) (port.Decision, error) {
	if r.Principal.Type == port.PrincipalServiceAccount {
		if slices.Contains(r.Principal.Scopes, r.Action) {
			return port.Decision{Allowed: true}, nil
		}
		return port.Decision{Allowed: false, Reasons: []string{"scope_missing"}}, nil
	}
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
