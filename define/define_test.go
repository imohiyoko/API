package define_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/imohiyoko/API/define"
)

type src struct {
	path  map[string]string
	query map[string][]string
	body  string
	aud   map[string]bool // 見える min
}

func (s src) PathValue(n string) string { return s.path[n] }
func (s src) QueryValues(n string) ([]string, bool) {
	v, ok := s.query[n]
	return v, ok
}
func (s src) Body() (map[string]json.RawMessage, error) {
	if s.body == "" {
		return nil, nil
	}
	var m map[string]json.RawMessage
	return m, json.Unmarshal([]byte(s.body), &m)
}
func (s src) Visible(min string) bool { return min == "" || s.aud[min] }

type Address struct {
	City string `api:"city"`
	Zip  string `api:"zip,optional"`
}

type In struct {
	ID      string    `api:"id,path"`
	Tags    []string  `api:"tags,body"`
	At      time.Time `api:"at,body"`
	Address Address   `api:"address,body"`
	Secret  string    `api:"secret,body,min=internal,optional"`
	Hidden  string    // タグが無いので出さない
}

func newOp() *define.Op[In, In] {
	return define.Command[In, In]("things.update").Handle(func(_ context.Context, in In) (In, error) { return in, nil })
}

func TestDecodeUsesAPINames(t *testing.T) {
	op := newOp()
	in, err := op.Decode(src{
		path: map[string]string{"id": "t1"},
		body: `{"tags":["a"],"at":"2026-11-02T00:00:00Z","address":{"city":"Tokyo"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	v := in.(In)
	if v.ID != "t1" || v.Tags[0] != "a" || v.Address.City != "Tokyo" || v.At.Year() != 2026 {
		t.Fatalf("got %+v", v)
	}
	if op.ResourceID(in, "id") != "t1" {
		t.Fatal("ResourceID")
	}
}

func TestDecodeErrors(t *testing.T) {
	op := newOp()
	cases := map[string]src{
		"nested unknown":      {path: map[string]string{"id": "x"}, body: `{"tags":[],"at":"2026-11-02T00:00:00Z","address":{"city":"a","country":"b"}}`},
		"nested missing":      {path: map[string]string{"id": "x"}, body: `{"tags":[],"at":"2026-11-02T00:00:00Z","address":{}}`},
		"invisible field set": {path: map[string]string{"id": "x"}, body: `{"tags":[],"at":"2026-11-02T00:00:00Z","address":{"city":"a"},"secret":"s"}`},
		"bad time":            {path: map[string]string{"id": "x"}, body: `{"tags":[],"at":"yesterday","address":{"city":"a"}}`},
	}
	for name, s := range cases {
		_, err := op.Decode(s)
		var e *define.Error
		if err == nil || !errorsAs(err, &e) || e.Code != define.CodeInvalidArgument {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// internal には secret が見える。
	ok := src{path: map[string]string{"id": "x"}, body: `{"tags":[],"at":"2026-11-02T00:00:00Z","address":{"city":"a"},"secret":"s"}`, aud: map[string]bool{"internal": true}}
	if _, err := op.Decode(ok); err != nil {
		t.Errorf("internal: %v", err)
	}
}

func errorsAs(err error, e **define.Error) bool {
	v, ok := err.(*define.Error)
	*e = v
	return ok
}

func TestSchemaFromTags(t *testing.T) {
	m, err := define.NewRegistry().Add(newOp()).Manifest()
	if err != nil {
		t.Fatal(err)
	}
	in := m.Operations[0].Input
	var names []string
	for _, f := range in.Fields {
		names = append(names, f.Name+":"+f.In+":"+f.Schema.Type)
	}
	want := "id:path:string tags:body:array at:body:string address:body:object secret:body:string"
	if strings.Join(names, " ") != want {
		t.Fatalf("got %s", strings.Join(names, " "))
	}
	if in.Fields[2].Schema.Format != "date-time" || !in.Fields[3].Schema.Fields[1].Optional || in.Fields[4].Min != "internal" {
		t.Fatalf("details: %+v", in.Fields)
	}
}

type withMap struct {
	M map[string]string `api:"m"`
}

type recursive struct {
	Child *recursive `api:"child"`
}

type badOption struct {
	X string `api:"x,sometimes"`
}

func TestUnsupportedTypesAreErrors(t *testing.T) {
	h1 := define.Query[withMap, string]("a.get").Handle(func(context.Context, withMap) (string, error) { return "", nil })
	h2 := define.Query[struct{}, recursive]("b.get").Handle(func(context.Context, struct{}) (recursive, error) { return recursive{}, nil })
	h3 := define.Query[badOption, string]("c.get").Handle(func(context.Context, badOption) (string, error) { return "", nil })
	h4 := define.Query[string, string]("d.get").Handle(func(context.Context, string) (string, error) { return "", nil })
	for _, op := range []define.Operation{h1, h2, h3, h4} {
		if _, err := op.Spec(); err == nil {
			s, _ := op.Spec()
			t.Errorf("%s: want an error", s.Name)
		}
	}
}

func TestEncodeProjects(t *testing.T) {
	v, err := define.Encode([]In{{ID: "1", Secret: "s", Hidden: "h", Tags: []string{"x"}}}, func(min string) bool { return min == "" })
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "Hidden") || !strings.Contains(string(b), `"id":"1"`) {
		t.Fatalf("got %s", b)
	}
}
