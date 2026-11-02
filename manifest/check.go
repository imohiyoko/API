package manifest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Severity は検査の結果の重さ（design.md 4.3）。
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Diagnostic は検査の結果の1件。
type Diagnostic struct {
	Severity  Severity `json:"severity"`
	Operation string   `json:"operation,omitempty"`
	Message   string   `json:"message"`
}

func (d Diagnostic) String() string {
	if d.Operation == "" {
		return fmt.Sprintf("%s: %s", d.Severity, d.Message)
	}
	return fmt.Sprintf("%s: %s: %s", d.Severity, d.Operation, d.Message)
}

// HasErrors は Error が1つでもあるか。
func HasErrors(ds []Diagnostic) bool {
	return slices.ContainsFunc(ds, func(d Diagnostic) bool { return d.Severity == Error })
}

var (
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	audienceRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// allowed は操作の種類とプロトコルの組み合わせ（design.md 5.1。表の「―」は false）。
var allowed = map[string]map[string]bool{
	KindQuery:   {REST: true, GraphQL: true, GRPC: true, SOAP: true},
	KindCommand: {REST: true, GraphQL: true, GRPC: true, SOAP: true},
	KindStream:  {REST: true, GraphQL: true, GRPC: true, WebSocket: true},
	KindEvent:   {GraphQL: true, GRPC: true, WebSocket: true, Webhook: true},
}

// Check はマニフェストを検査する（design.md 4.3）。
func Check(m *Manifest) []Diagnostic {
	var ds []Diagnostic
	add := func(sev Severity, op, format string, args ...any) {
		ds = append(ds, Diagnostic{Severity: sev, Operation: op, Message: fmt.Sprintf(format, args...)})
	}
	auds := m.AudienceList()
	seenAud := map[string]bool{}
	for _, a := range auds {
		if !audienceRe.MatchString(a) {
			add(Error, "", "invalid audience name %q", a)
		}
		if seenAud[a] {
			add(Error, "", "duplicate audience %q", a)
		}
		seenAud[a] = true
	}
	narrowest := auds[len(auds)-1]
	if m.Name != "" && !nameRe.MatchString(m.Name) {
		add(Error, "", "invalid API name %q", m.Name)
	}

	seenOp := map[string]bool{}
	routes := map[string]string{}       // audience + route key → operation
	grpcNames := map[string]grpcIdent{} // audience + gRPC name → operation
	for i := range m.Operations {
		op := &m.Operations[i]
		n := op.Name
		if !nameRe.MatchString(n) {
			add(Error, n, "invalid operation name (use lowercase words joined by dots, e.g. users.get)")
		}
		if seenOp[n] {
			add(Error, n, "duplicate operation name")
		}
		seenOp[n] = true
		if allowed[op.Kind] == nil {
			add(Error, n, "unknown kind %q", op.Kind)
			continue
		}
		if op.Authz != nil && op.Anonymous {
			add(Error, n, "an operation cannot have both Authz and Anonymous")
		}
		if op.Authz != nil {
			if op.Authz.Action == "" {
				add(Error, n, "Authz needs an action")
			}
			if op.Authz.ResourceField != "" && !hasField(op.Input, op.Authz.ResourceField) {
				add(Error, n, "Authz resource field %q is not an input field", op.Authz.ResourceField)
			}
		}
		if op.Input.Type != "object" || op.Output.Type == "" {
			add(Error, n, "input must be an object and output must have a type")
		}
		checkInput(op, add)

		exposedTo := map[string]bool{}
		for _, e := range op.Expose {
			if !seenAud[e.Audience] {
				add(Error, n, "unknown audience %q", e.Audience)
				continue
			}
			exposedTo[e.Audience] = true
			if e.Audience == narrowest && op.Authz == nil && !op.Anonymous {
				add(Error, n, "exposed to %s without Authz or Anonymous", e.Audience)
			}
			for _, p := range e.Protocols {
				if !slices.Contains(Protocols, p) {
					add(Error, n, "unknown protocol %q", p)
					continue
				}
				if !allowed[op.Kind][p] {
					add(Error, n, "a %s cannot be exposed over %s", op.Kind, p)
					continue
				}
				if !Implemented[p] {
					add(Warning, n, "%s is not implemented yet; the %s exposure is ignored", p, e.Audience)
					continue
				}
				if p == GRPC {
					if !GRPCServes(op.Kind) {
						add(Warning, n, "%s", grpcIgnored(op.Kind, e.Audience))
						continue
					}
					checkGRPC(op, e.Audience, grpcNames, add)
				}
				if p == REST {
					method, path, err := op.ResolveHTTP()
					if err != nil {
						add(Error, n, "REST: %v", err)
						continue
					}
					checkPath(op, path, add)
					key := e.Audience + " " + RouteKey(method, path)
					if other, ok := routes[key]; ok {
						add(Error, n, "REST route %s %s on %s conflicts with %s", method, path, e.Audience, other)
					} else {
						routes[key] = n
					}
				}
			}
		}
		seenRate := map[string]bool{}
		for _, r := range op.RateLimits {
			switch {
			case !seenAud[r.Audience]:
				add(Error, n, "rate limit for unknown audience %q", r.Audience)
			case seenRate[r.Audience]:
				add(Error, n, "duplicate rate limit for %s", r.Audience)
			case !exposedTo[r.Audience]:
				add(Warning, n, "rate limit for %s, where the operation is not exposed", r.Audience)
			}
			seenRate[r.Audience] = true
			if _, err := ParseRate(r.Limit); err != nil {
				add(Error, n, "%v", err)
			}
		}
		if len(exposedTo) > 0 {
			checkMin(m, n, op.Input, exposedTo, add)
			checkMin(m, n, op.Output, exposedTo, add)
		}
	}
	return ds
}

func hasField(s Schema, name string) bool {
	return slices.ContainsFunc(s.Fields, func(f Field) bool { return f.Name == name })
}

func checkInput(op *Operation, add func(Severity, string, string, ...any)) {
	seen := map[string]bool{}
	for _, f := range op.Input.Fields {
		if seen[f.Name] {
			add(Error, op.Name, "duplicate input field %q", f.Name)
		}
		seen[f.Name] = true
		switch f.In {
		case "path", "query":
			if f.Schema.Type == "object" || f.Schema.Type == "array" {
				add(Error, op.Name, "input field %q in %s must be a scalar", f.Name, f.In)
			}
			if f.In == "path" && f.Optional {
				add(Error, op.Name, "path field %q cannot be optional", f.Name)
			}
		case "body":
			if op.Kind == KindQuery {
				add(Error, op.Name, "a query cannot have body field %q (use query or path)", f.Name)
			}
		default:
			add(Error, op.Name, "input field %q has unknown location %q", f.Name, f.In)
		}
	}
}

func checkPath(op *Operation, path string, add func(Severity, string, string, ...any)) {
	params := PathParams(path)
	for _, p := range params {
		ok := slices.ContainsFunc(op.Input.Fields, func(f Field) bool { return f.Name == p && f.In == "path" })
		if !ok {
			add(Error, op.Name, "path parameter {%s} has no path input field", p)
		}
	}
	for _, f := range op.Input.Fields {
		if f.In == "path" && !slices.Contains(params, f.Name) {
			add(Error, op.Name, "path field %q is not in the path %s", f.Name, path)
		}
	}
	if !strings.HasPrefix(path, "/") {
		add(Error, op.Name, "path %q must start with /", path)
	}
}

// checkMin は、フィールドの min が知らない公開範囲でないか、出すどの公開範囲よりも狭くないか（警告）を調べる。
func checkMin(m *Manifest, op string, s Schema, exposedTo map[string]bool, add func(Severity, string, string, ...any)) {
	for _, f := range s.Fields {
		if f.Min != "" {
			if m.Rank(f.Min) < 0 {
				add(Error, op, "field %q has unknown min audience %q", f.Name, f.Min)
			} else {
				visible := false
				for a := range exposedTo {
					if m.Visible(f.Min, a) {
						visible = true
					}
				}
				if !visible {
					add(Warning, op, "field %q (min=%s) is not visible on any audience the operation is exposed to", f.Name, f.Min)
				}
			}
		}
		checkMin(m, op, f.Schema, exposedTo, add)
	}
	if s.Items != nil {
		checkMin(m, op, *s.Items, exposedTo, add)
	}
}
