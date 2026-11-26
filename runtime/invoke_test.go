package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/port"
	"github.com/imohiyoko/API/runtime"
)

// invokeOutcome は Invoke の結果を、REST の結果（restOutcome）と比べられる形にする。
func invokeOutcome(t *testing.T) func(out any, err error) outcome {
	return func(out any, err error) outcome { return toOutcome(t, out, err) }
}

func toOutcome(t *testing.T, out any, err error) outcome {
	t.Helper()
	if err != nil {
		var e *define.Error
		if !errors.As(err, &e) {
			t.Fatalf("Invoke returned %T, want *define.Error: %v", err, err)
		}
		return outcome{Code: string(e.Code), Message: e.Message, Reasons: e.Reasons}
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	_ = json.Unmarshal(b, &v)
	return outcome{Value: normalize(v)}
}

// roadmap.md M3.5：同じ操作を REST と Invoke で呼ぶと、同じ入力・主体で同じ出力とエラーになる。
func TestRESTAndInvokeAgree(t *testing.T) {
	srv := parityServer(t)
	full := `{"tags":["x","y"],"at":"2026-11-02T00:00:00Z","address":{"city":"Tokyo","zip":"100"},"count":3`
	cases := []struct {
		name, aud, token, id, query string
		body                        string // REST の本文（Invoke では id・dry を足す）
	}{
		{"internal ok", "internal", "internal", "t1", "?dry=true", full + `,"secret":"s"}`},
		{"public ok", "public", "user", "t1", "", full + `}`},
		{"no token", "public", "", "t1", "", full + `}`},
		{"user on internal", "internal", "user", "t1", "", full + `}`},
		{"denied", "public", "user-denied", "t1", "", full + `}`},
		{"denied with reasons", "internal", "internal", "t1", "", full + `}`},
		{"missing nested", "public", "user", "t1", "", `{"tags":[],"at":"2026-11-02T00:00:00Z"}`},
		// JSON の入力は、gRPC と違って、必須のフィールドが無いことを REST と同じく拒む。
		{"missing scalar", "public", "user", "t1", "", `{"tags":[],"at":"2026-11-02T00:00:00Z","address":{"city":"x"}}`},
		{"unknown field", "public", "user", "t1", "", full + `,"nope":1}`},
		{"internal field on public", "public", "user", "t1", "", full + `,"secret":"s"}`},
		{"wrong type", "public", "user", "t1", "", `{"tags":"x","at":"2026-11-02T00:00:00Z","address":{"city":"x"},"count":1}`},
		{"not found", "internal", "internal", "missing", "", full + `}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rest := restOutcome(t, srv.Handler(c.aud), "PATCH", "/things/"+c.id+c.query, c.token, c.body)
			var in map[string]any
			if err := json.Unmarshal([]byte(c.body), &in); err != nil {
				t.Fatal(err)
			}
			in["id"] = c.id
			if c.query != "" {
				in["dry"] = true
			}
			b, _ := json.Marshal(in)
			got := invokeOutcome(t)(srv.Invoke(context.Background(), c.aud, "things.update", c.token, json.RawMessage(b)))
			if !reflect.DeepEqual(rest, got) {
				t.Errorf("REST   %+v\nInvoke %+v", rest, got)
			}
		})
	}
	// オブジェクトでない出力と、トークンの無い呼び出し。
	rest := restOutcome(t, srv.Handler("public"), "GET", "/things/names", "", "")
	got := invokeOutcome(t)(srv.Invoke(context.Background(), "public", "things.names", "", nil))
	if !reflect.DeepEqual(rest, got) {
		t.Errorf("names: REST %+v Invoke %+v", rest, got)
	}
}

// Go の値の入力も、JSON と同じ結果になる。公開範囲に見えないフィールドは無いものとして扱う。
func TestInvokeWithGoValue(t *testing.T) {
	srv := parityServer(t)
	dry, zip, secret := true, "100", "s"
	in := thingIn{ID: "t1", Dry: &dry, Tags: []string{"x"}, At: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		Address: address{City: "Tokyo", Zip: &zip}, Count: 3, Secret: &secret}
	body := `{"tags":["x"],"at":"2026-11-02T00:00:00Z","address":{"city":"Tokyo","zip":"100"},"count":3`
	for _, c := range []struct{ aud, token, body string }{
		{"internal", "internal", body + `,"secret":"s"}`},
		{"public", "user", body + `}`},
		{"public", "user-denied", body + `}`},
	} {
		rest := restOutcome(t, srv.Handler(c.aud), "PATCH", "/things/t1?dry=true", c.token, c.body)
		for name, v := range map[string]any{"value": in, "pointer": &in} {
			got := invokeOutcome(t)(srv.Invoke(context.Background(), c.aud, "things.update", c.token, v))
			if !reflect.DeepEqual(rest, got) {
				t.Errorf("%s %s %s: REST %+v\nInvoke %+v", c.aud, c.token, name, rest, got)
			}
		}
	}
	if _, err := srv.Invoke(context.Background(), "public", "things.update", "user", 42); err == nil {
		t.Error("a number is not an input")
	}
	if _, err := srv.Invoke(context.Background(), "public", "things.update", "user", []byte(`[1]`)); err == nil {
		t.Error("a JSON array is not an input")
	}
}

// roadmap.md M3.5：Expose の無い公開範囲への Invoke は拒まれる（REST と同じく not_found）。
func TestInvokeOnlyOnExposedAudiences(t *testing.T) {
	srv, _ := newServer(t)
	ctx := context.Background()
	for _, c := range []struct{ aud, op, token string }{
		{"public", "users.create", "user"}, // internal にだけ出す
		{"public", "users.list", "user"},   // internal・partner にだけ出す
		{"nope", "users.get", "user"},      // 知らない公開範囲
		{"internal", "users.nope", "internal"},
		{"", "users.get", "internal"}, // 公開範囲は必ず渡す
	} {
		_, err := srv.Invoke(ctx, c.aud, c.op, c.token, users.CreateUserIn{Name: "x", Email: "y"})
		var e *define.Error
		if !errors.As(err, &e) || e.Code != define.CodeNotFound {
			t.Errorf("%s %s: got %v, want not_found", c.aud, c.op, err)
		}
	}
	// 認証より前に拒む（REST の 404 と同じく、トークンが無くても not_found）。
	if _, err := srv.Invoke(ctx, "public", "users.create", "", nil); !isCode(err, define.CodeNotFound) {
		t.Errorf("no token: got %v", err)
	}
}

// roadmap.md M3.5：min=partner のフィールドが public の Invoke の出力に出ない。
func TestInvokeProjectsFields(t *testing.T) {
	srv, _ := newServer(t)
	ctx := context.Background()
	out, err := srv.Invoke(ctx, "internal", "users.create", "internal", users.CreateUserIn{Name: "Ada", Email: "ada@example.com", Note: "vip"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["id"].(string)
	for _, c := range []struct {
		aud, token string
		want       []string
		absent     []string
	}{
		{"internal", "internal", []string{"id", "name", "email", "note"}, nil},
		{"partner", "partner", []string{"id", "name", "email"}, []string{"note"}},
		{"public", "user", []string{"id", "name"}, []string{"email", "note"}},
	} {
		out, err := srv.Invoke(ctx, c.aud, "users.get", c.token, users.GetUserIn{ID: id})
		if err != nil {
			t.Fatalf("%s: %v", c.aud, err)
		}
		got := out.(map[string]any)
		for _, k := range c.want {
			if _, ok := got[k]; !ok {
				t.Errorf("%s: missing %q in %v", c.aud, k, got)
			}
		}
		for _, k := range c.absent {
			if _, ok := got[k]; ok {
				t.Errorf("%s: %q must not be visible: %v", c.aud, k, got)
			}
		}
	}
}

// Invoke も、認証・主体の確認・開示のルールを飛ばさない。
func TestInvokeAuthentication(t *testing.T) {
	srv, _ := newServer(t)
	ctx := context.Background()
	for _, c := range []struct {
		aud, token string
		want       define.Code
	}{
		{"internal", "", define.CodeUnauthenticated},
		{"internal", "bogus", define.CodeUnauthenticated},
		{"internal", "user", define.CodePermissionDenied}, // internal の口はユーザーを受けない
		{"partner", "partner-no-org", define.CodePermissionDenied},
	} {
		_, err := srv.Invoke(ctx, c.aud, "users.list", c.token, nil)
		if !isCode(err, c.want) {
			t.Errorf("%s %q: got %v, want %s", c.aud, c.token, err, c.want)
		}
	}
	// public はコードだけを見せる（ハンドラーのメッセージを出さない）。
	_, err := srv.Invoke(ctx, "public", "users.get", "user", users.GetUserIn{ID: "usr_404"})
	var e *define.Error
	if !errors.As(err, &e) || e.Code != define.CodeNotFound || e.Message != "" {
		t.Errorf("public not found: %#v", err)
	}
}

func isCode(err error, code define.Code) bool {
	var e *define.Error
	return errors.As(err, &e) && e.Code == code
}

// パスの引数に複数の値を渡すと、先頭だけを使わずに拒む。
func TestInvokeRejectsMultiplePathValues(t *testing.T) {
	srv := parityServer(t)
	body := `{"id":["t1","missing"],"tags":[],"at":"2026-11-02T00:00:00Z","address":{"city":"x"},"count":1}`
	if _, err := srv.Invoke(context.Background(), "public", "things.update", "user", json.RawMessage(body)); !isCode(err, define.CodeInvalidArgument) {
		t.Errorf("two ids: got %v", err)
	}
	one := `{"id":["t1"],"tags":[],"at":"2026-11-02T00:00:00Z","address":{"city":"x"},"count":1}`
	if _, err := srv.Invoke(context.Background(), "public", "things.update", "user", json.RawMessage(one)); err != nil {
		t.Errorf("one id in a list: %v", err)
	}
}

// トークンの無い呼び出しは、呼び出し元の ctx の主体を引き継がない。
func TestInvokeAnonymousDropsCallerPrincipal(t *testing.T) {
	var seen bool
	op := define.Query[struct{}, string]("who.am_i", define.Anonymous(), define.Expose(define.Public, define.REST)).
		Handle(func(ctx context.Context, _ struct{}) (string, error) {
			_, seen = define.PrincipalFrom(ctx)
			return "ok", nil
		})
	srv, err := runtime.Build(define.NewRegistry().Add(op), runtime.Config{Authenticator: memory.NewAuthenticator(), Authorizer: memory.NewAuthorizer()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := define.WithPrincipal(context.Background(), port.Principal{Type: port.PrincipalUser, ID: "usr_parent"})
	if _, err := srv.Invoke(ctx, "public", "who.am_i", "", nil); err != nil {
		t.Fatal(err)
	}
	if seen {
		t.Error("anonymous call saw the caller's principal")
	}
}
