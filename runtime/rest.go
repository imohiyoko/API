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
		h := &restOp{opCall{m: m, aud: aud, ac: ac, spec: spec, op: ops[spec.Name], cfg: cfg}}
		mux.Handle(method+" "+path, h)
	}
	return mux
}

type restOp struct {
	opCall
}

func (h *restOp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			h.cfg.Logger.Error("api handler panic", "operation", h.spec.Name, "panic", v)
			h.writeError(w, define.Errorf(define.CodeInternal, "handler panicked"))
		}
	}()
	token, hasToken := bearer(r.Header.Get("Authorization"))
	body, err := h.run(r.Context(), token, hasToken, &restSource{r: r, m: h.m, aud: h.aud, needBody: hasBody(h.spec)})
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// errorBody は REST のエラーの本文。見せる中身は公開範囲の Disclosure で決まる。
type errorBody struct {
	Error   string   `json:"error"`
	Message string   `json:"message,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

func (h *restOp) writeError(w http.ResponseWriter, err error) {
	e := h.failure(err)
	msg, reasons := h.disclose(e)
	if e.Code == define.CodeUnauthenticated {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	writeJSON(w, define.HTTPStatus(e.Code), errorBody{Error: string(e.Code), Message: msg, Reasons: reasons})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// bearer は Authorization の値から Bearer のトークンを取り出す。
func bearer(h string) (string, bool) {
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
