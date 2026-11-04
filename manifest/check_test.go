package manifest_test

import (
	"strings"
	"testing"

	"github.com/imohiyoko/API/manifest"
)

func str() manifest.Schema { return manifest.Schema{Type: "string"} }

func op(name, kind string, mod func(*manifest.Operation)) manifest.Operation {
	o := manifest.Operation{
		Name: name, Kind: kind,
		Authz:  &manifest.Authz{Action: "x:y"},
		Input:  manifest.Schema{Type: "object"},
		Output: str(),
		Expose: []manifest.Exposure{{Audience: "public", Protocols: []string{"rest"}}},
	}
	if mod != nil {
		mod(&o)
	}
	return o
}

// roadmap.md M1：design.md 4.3 の不正な定義を、すべて報告する。
func TestCheckReportsEveryRule(t *testing.T) {
	cases := []struct {
		name string
		ops  []manifest.Operation
		sev  manifest.Severity
		want string
	}{
		{"kind x protocol", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Expose[0].Protocols = []string{"webhook"}
		})}, manifest.Error, "cannot be exposed over webhook"},
		{"public without authz", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) { o.Authz = nil })},
			manifest.Error, "without Authz or Anonymous"},
		{"partner without authz", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Authz = nil
			o.Expose[0].Audience = "partner"
		})}, manifest.Error, "exposed to partner without Authz or Anonymous"},
		{"internal without authz", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Authz = nil
			o.Expose[0].Audience = "internal"
		})}, manifest.Error, "exposed to internal without Authz or Anonymous"},
		{"command method", []manifest.Operation{op("a.archive", "command", nil)}, manifest.Error, "cannot decide the HTTP method"},
		{"route conflict", []manifest.Operation{op("a.get", "query", nil), op("b.get", "query", func(o *manifest.Operation) {
			o.HTTP = &manifest.HTTP{Path: "/a"}
		})}, manifest.Error, "conflicts with a.get"},
		{"path params conflict", []manifest.Operation{
			op("a.get", "query", func(o *manifest.Operation) {
				o.Input.Fields = []manifest.Field{{Name: "id", In: "path", Schema: str()}}
			}),
			op("a.find", "query", func(o *manifest.Operation) {
				o.HTTP = &manifest.HTTP{Path: "/a/{key}"}
				o.Input.Fields = []manifest.Field{{Name: "key", In: "path", Schema: str()}}
			}),
		}, manifest.Error, "conflicts with a.get"},
		{"invisible field", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Output = manifest.Schema{Type: "object", Fields: []manifest.Field{{Name: "secret", Min: "internal", Schema: str()}}}
		})}, manifest.Warning, "not visible on any audience"},
		{"not implemented", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Expose[0].Protocols = []string{"graphql"}
		})}, manifest.Warning, "not implemented yet"},
		{"grpc stream not implemented", []manifest.Operation{op("a.watch", "stream", func(o *manifest.Operation) {
			o.Expose[0].Protocols = []string{"grpc"}
		})}, manifest.Warning, "gRPC for a stream is not implemented yet"},
		{"grpc name conflict", []manifest.Operation{
			op("a_b.c", "query", func(o *manifest.Operation) { o.Expose[0].Protocols = []string{"grpc"} }),
			op("a.b_c", "query", func(o *manifest.Operation) { o.Expose[0].Protocols = []string{"grpc"} }),
		}, manifest.Error, "conflicts with a_b.c"},
		{"grpc service vs message", []manifest.Operation{
			op("users_request.get", "query", func(o *manifest.Operation) { o.Expose[0].Protocols = []string{"grpc"} }),
			op("users", "query", func(o *manifest.Operation) { o.Expose[0].Protocols = []string{"grpc"} }),
		}, manifest.Error, "UsersRequest on public conflicts with users_request.get"},
		{"grpc array of arrays", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Expose[0].Protocols = []string{"grpc"}
			inner := manifest.Schema{Type: "array", Items: &manifest.Schema{Type: "string"}}
			o.Output = manifest.Schema{Type: "array", Items: &inner}
		})}, manifest.Error, "array of arrays"},
		{"grpc field name", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Expose[0].Protocols = []string{"grpc"}
			o.Output = manifest.Schema{Type: "object", Fields: []manifest.Field{{Name: "my-field", Schema: str()}}}
		})}, manifest.Error, "not a valid protobuf name"},
		{"bad rate", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.RateLimits = []manifest.RateLimit{{Audience: "public", Limit: "10/day"}}
		})}, manifest.Error, "unit must be"},
		{"rate unknown audience", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.RateLimits = []manifest.RateLimit{{Audience: "galaxy", Limit: "1/s"}}
		})}, manifest.Error, "rate limit for unknown audience"},
		{"rate not exposed", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.RateLimits = []manifest.RateLimit{{Audience: "partner", Limit: "1/s"}}
		})}, manifest.Warning, "not exposed"},
		{"bad name", []manifest.Operation{op("A.Get", "query", nil)}, manifest.Error, "invalid operation name"},
		{"unknown audience", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Expose[0].Audience = "galaxy"
		})}, manifest.Error, "unknown audience"},
		{"body on query", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Input.Fields = []manifest.Field{{Name: "x", In: "body", Schema: str()}}
		})}, manifest.Error, "cannot have body field"},
		{"path field missing from path", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.HTTP = &manifest.HTTP{Path: "/a"}
			o.Input.Fields = []manifest.Field{{Name: "id", In: "path", Schema: str()}}
		})}, manifest.Error, "is not in the path"},
		{"authz resource field", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Authz.ResourceField = "id"
		})}, manifest.Error, "is not an input field"},
		{"authz and anonymous", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) { o.Anonymous = true })},
			manifest.Error, "both Authz and Anonymous"},
		{"duplicate", []manifest.Operation{op("a.get", "query", nil), op("a.get", "query", nil)}, manifest.Error, "duplicate operation name"},
		{"unknown min", []manifest.Operation{op("a.get", "query", func(o *manifest.Operation) {
			o.Output = manifest.Schema{Type: "object", Fields: []manifest.Field{{Name: "x", Min: "vip", Schema: str()}}}
		})}, manifest.Error, "unknown min audience"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ds := manifest.Check(&manifest.Manifest{Version: manifest.Version, Operations: c.ops})
			for _, d := range ds {
				if d.Severity == c.sev && strings.Contains(d.Message, c.want) {
					if c.sev == manifest.Warning && manifest.HasErrors(ds) {
						t.Errorf("a warning case must not have errors: %v", ds)
					}
					return
				}
			}
			t.Errorf("want %s containing %q, got %v", c.sev, c.want, ds)
		})
	}
}

func TestCheckAcceptsValidManifest(t *testing.T) {
	m := &manifest.Manifest{Version: manifest.Version, Operations: []manifest.Operation{
		op("users.get", "query", func(o *manifest.Operation) {
			o.Input.Fields = []manifest.Field{{Name: "id", In: "path", Schema: str()}}
		}),
		op("users.create", "command", nil),
		op("users.delete", "command", func(o *manifest.Operation) {
			o.Input.Fields = []manifest.Field{{Name: "id", In: "path", Schema: str()}}
		}),
	}}
	if ds := manifest.Check(m); len(ds) != 0 {
		t.Fatalf("unexpected diagnostics: %v", ds)
	}
}

func TestResolveHTTP(t *testing.T) {
	path := []manifest.Field{{Name: "id", In: "path", Schema: str()}}
	cases := []struct {
		name, kind   string
		http         *manifest.HTTP
		fields       []manifest.Field
		method, want string
	}{
		{"users.get", "query", nil, path, "GET", "/users/{id}"},
		{"users.list", "query", nil, nil, "GET", "/users"},
		{"users.search", "query", nil, nil, "GET", "/users/search"},
		{"users.create", "command", nil, nil, "POST", "/users"},
		{"users.replace", "command", nil, path, "PUT", "/users/{id}"},
		{"users.update", "command", nil, path, "PATCH", "/users/{id}"},
		{"users.delete", "command", nil, path, "DELETE", "/users/{id}"},
		{"users.archive", "command", &manifest.HTTP{Method: "post"}, path, "POST", "/users/archive/{id}"},
		{"users.get", "query", &manifest.HTTP{Path: "/v1/u/{id}"}, path, "GET", "/v1/u/{id}"},
	}
	for _, c := range cases {
		o := manifest.Operation{Name: c.name, Kind: c.kind, HTTP: c.http, Input: manifest.Schema{Type: "object", Fields: c.fields}}
		m, p, err := o.ResolveHTTP()
		if err != nil || m != c.method || p != c.want {
			t.Errorf("%s: got %s %s %v, want %s %s", c.name, m, p, err, c.method, c.want)
		}
	}
}

func TestVisible(t *testing.T) {
	m := &manifest.Manifest{}
	cases := []struct {
		min, aud string
		want     bool
	}{
		{"", "public", true}, {"public", "public", true}, {"partner", "public", false},
		{"partner", "partner", true}, {"partner", "internal", true}, {"internal", "partner", false},
	}
	for _, c := range cases {
		if got := m.Visible(c.min, c.aud); got != c.want {
			t.Errorf("Visible(%q, %q) = %v", c.min, c.aud, got)
		}
	}
}

func TestReadRejectsUnknownVersionAndKeys(t *testing.T) {
	if _, err := manifest.Unmarshal([]byte(`{"version":"other","operations":[]}`)); err == nil {
		t.Error("want an error for an unknown version")
	}
	if _, err := manifest.Unmarshal([]byte(`{"version":"` + manifest.Version + `","operations":[],"extra":1}`)); err == nil {
		t.Error("want an error for an unknown key")
	}
}
