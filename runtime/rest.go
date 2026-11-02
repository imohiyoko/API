package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/manifest"
	"github.com/imohiyoko/API/port"
)

// maxBody は本文の上限。
const maxBody = 1 << 20

func newRESTHandler(m *manifest.Manifest, aud string, ac AudienceConfig, ops map[string]define.Operation, cfg Config) http.Handler {
	mux := http.NewServeMux()
	for i := range m.Operations {
		spec := &m.Operations[i]
		if !spec.Exposed(aud, manifest.REST) {
			continue
		}
		method, path, _ := spec.ResolveHTTP() // Check 済み
		h := &restOp{m: m, aud: aud, ac: ac, spec: spec, op: ops[spec.Name], cfg: cfg}
		mux.Handle(method+" "+path, h)
	}
	return mux
}

type restOp struct {
	m    *manifest.Manifest
	aud  string
	ac   AudienceConfig
	spec *manifest.Operation
	op   define.Operation
	cfg  Config
}

func (h *restOp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			h.cfg.Logger.Error("api handler panic", "operation", h.spec.Name, "panic", v)
			h.writeError(w, define.Errorf(define.CodeInternal, "handler panicked"))
		}
	}()
	out, err := h.serve(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	body, err := define.Encode(out, func(min string) bool { return h.m.Visible(min, h.aud) })
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *restOp) serve(r *http.Request) (any, error) {
	ctx := define.WithAudience(r.Context(), h.aud)

	// 認証。Anonymous でも、トークンが付いていれば確かめる。
	token, hasToken := bearer(r)
	var principal *port.Principal
	if hasToken {
		p, err := h.cfg.Authenticator.Authenticate(ctx, token)
		if errors.Is(err, port.ErrUnauthenticated) {
			return nil, &define.Error{Code: define.CodeUnauthenticated}
		}
		if err != nil {
			return nil, h.unavailable("authenticate", err)
		}
		if !slices.Contains(h.ac.Principals, p.Type) || (h.ac.RequireOrg && p.OrgID == "") {
			// この口で受けない主体（例：internal の口にユーザーのトークン）。
			return nil, &define.Error{Code: define.CodePermissionDenied, Reasons: []string{"principal_not_allowed"}}
		}
		principal = &p
		ctx = define.WithPrincipal(ctx, p)
	} else if !h.spec.Anonymous {
		return nil, &define.Error{Code: define.CodeUnauthenticated}
	}

	in, err := h.op.Decode(&restSource{r: r, m: h.m, aud: h.aud, needBody: hasBody(h.spec)})
	if err != nil {
		return nil, err
	}

	// 認可（iam の authz:check）。
	if a := h.spec.Authz; a != nil {
		req := port.CheckRequest{Principal: *principal, Action: a.Action, ResourceType: a.ResourceType}
		if a.ResourceField != "" {
			req.ResourceID = h.op.ResourceID(in, a.ResourceField)
		}
		d, err := h.cfg.Authorizer.Check(ctx, req)
		if err != nil {
			return nil, h.unavailable("authorize", err)
		}
		if !d.Allowed {
			return nil, &define.Error{Code: define.CodePermissionDenied, Reasons: d.Reasons}
		}
	}
	return h.op.Call(ctx, in)
}

func (h *restOp) unavailable(step string, err error) error {
	h.cfg.Logger.Error("api dependency failed", "operation", h.spec.Name, "step", step, "error", err)
	return define.Errorf(define.CodeUnavailable, "%s failed", step)
}

// errorBody は REST のエラーの本文。見せる中身は公開範囲の Disclosure で決まる。
type errorBody struct {
	Error   string   `json:"error"`
	Message string   `json:"message,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

func (h *restOp) writeError(w http.ResponseWriter, err error) {
	var e *define.Error
	if !errors.As(err, &e) {
		h.cfg.Logger.Error("api handler error", "operation", h.spec.Name, "error", err)
		e = &define.Error{Code: define.CodeInternal}
	}
	b := errorBody{Error: string(e.Code)}
	if h.ac.Disclosure >= DiscloseReasons {
		b.Reasons = e.Reasons
	}
	// 入力の誤りは、どの公開範囲でも直し方が分かるようにメッセージを見せる。
	if h.ac.Disclosure >= DiscloseDetail || e.Code == define.CodeInvalidArgument {
		b.Message = e.Message
	}
	if e.Code == define.CodeUnauthenticated {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	writeJSON(w, define.HTTPStatus(e.Code), b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func hasBody(op *manifest.Operation) bool {
	return slices.ContainsFunc(op.Input.Fields, func(f manifest.Field) bool { return f.In == "body" })
}

type restSource struct {
	r        *http.Request
	m        *manifest.Manifest
	aud      string
	needBody bool
	body     map[string]json.RawMessage
	read     bool
}

func (s *restSource) PathValue(name string) string { return s.r.PathValue(name) }

func (s *restSource) QueryValues(name string) ([]string, bool) {
	v, ok := s.r.URL.Query()[name]
	return v, ok
}

func (s *restSource) Visible(min string) bool { return s.m.Visible(min, s.aud) }

func (s *restSource) Body() (map[string]json.RawMessage, error) {
	if s.read {
		return s.body, nil
	}
	s.read = true
	raw, err := io.ReadAll(io.LimitReader(s.r.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBody {
		return nil, errors.New("too large")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	if !s.needBody {
		return nil, errors.New("this operation takes no body")
	}
	if ct := s.r.Header.Get("Content-Type"); ct != "" {
		if mt, _, _ := mime.ParseMediaType(ct); mt != "application/json" {
			return nil, errors.New("content type must be application/json")
		}
	}
	if err := json.Unmarshal(raw, &s.body); err != nil {
		return nil, errors.New("must be a JSON object")
	}
	return s.body, nil
}
