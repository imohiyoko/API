package runtime_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/gen/proto"
	"github.com/imohiyoko/API/port"
	"github.com/imohiyoko/API/runtime"
)

type address struct {
	City string  `api:"city"`
	Zip  *string `api:"zip"`
}

type thingIn struct {
	ID      string    `api:"id,path"`
	Dry     *bool     `api:"dry,query"`
	Tags    []string  `api:"tags,body"`
	At      time.Time `api:"at,body"`
	Address address   `api:"address,body"`
	Count   int32     `api:"count,body"`
	Secret  *string   `api:"secret,body,min=internal"`
}

type thingOut struct {
	ID      string    `api:"id"`
	Dry     bool      `api:"dry"`
	Tags    []string  `api:"tags"`
	At      time.Time `api:"at"`
	Address address   `api:"address"`
	Count   int32     `api:"count"`
	Ratio   float64   `api:"ratio"`
	Data    []byte    `api:"data"`
	Secret  *string   `api:"secret,min=internal"`
}

func parityServer(t *testing.T) *runtime.Server {
	t.Helper()
	update := define.Command[thingIn, thingOut]("things.update",
		define.Authz("things:update", define.Resource("thing", "id")),
		define.Expose(define.Internal, define.REST, define.GRPC),
		define.Expose(define.Public, define.REST, define.GRPC),
	).Handle(func(_ context.Context, in thingIn) (thingOut, error) {
		if in.ID == "missing" {
			return thingOut{}, define.Errorf(define.CodeNotFound, "no thing %s", in.ID)
		}
		return thingOut{ID: in.ID, Dry: in.Dry != nil && *in.Dry, Tags: in.Tags, At: in.At, Address: in.Address,
			Count: in.Count, Ratio: 0.5, Data: []byte("hi"), Secret: in.Secret}, nil
	})
	names := define.Query[struct{}, []string]("things.names",
		define.Anonymous(),
		define.Expose(define.Public, define.REST, define.GRPC),
	).Handle(func(context.Context, struct{}) ([]string, error) { return []string{"a", "b"}, nil })

	authn := memory.NewAuthenticator().
		Add("internal", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_int"}).
		Add("user", port.Principal{Type: port.PrincipalUser, ID: "usr_1"}).
		Add("user-denied", port.Principal{Type: port.PrincipalUser, ID: "usr_2"})
	authz := memory.NewAuthorizer()
	for _, id := range []string{"sa_int", "usr_1"} {
		authz.Allow(memory.Grant{PrincipalID: id, Action: "things:update"})
	}
	srv, err := runtime.Build(define.NewRegistry().Add(update).Add(names), runtime.Config{Authenticator: authn, Authorizer: authz})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

type grpcClient struct {
	conn *grpc.ClientConn
	file protoreflect.FileDescriptor
}

func dialGRPC(t *testing.T, srv *runtime.Server, aud string) *grpcClient {
	t.Helper()
	gs, err := srv.GRPCServer(aud)
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	file, err := proto.File(srv.Manifest(), aud)
	if err != nil {
		t.Fatal(err)
	}
	return &grpcClient{conn: conn, file: file}
}

// outcome はプロトコルによらない結果。値は JSON にして比べる（null は除く）。
type outcome struct {
	Code    string
	Message string
	Reasons []string
	Value   any
}

func (c *grpcClient) call(t *testing.T, service, method, token, input string) outcome {
	t.Helper()
	sd := c.file.Services().ByName(protoreflect.Name(service))
	md := sd.Methods().ByName(protoreflect.Name(method))
	req := dynamicpb.NewMessage(md.Input())
	if err := protojson.Unmarshal([]byte(input), req); err != nil {
		t.Fatalf("request %s: %v", input, err)
	}
	res := dynamicpb.NewMessage(md.Output())
	ctx := context.Background()
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}
	err := c.conn.Invoke(ctx, "/"+string(sd.FullName())+"/"+method, req, res)
	if err != nil {
		st := status.Convert(err)
		o := outcome{Message: st.Message()}
		for _, d := range st.Details() {
			if info, ok := d.(*errdetails.ErrorInfo); ok {
				o.Code = info.Reason
				if r := info.Metadata["reasons"]; r != "" {
					o.Reasons = strings.Split(r, ",")
				}
			}
		}
		if o.Message == o.Code {
			o.Message = ""
		}
		return o
	}
	b, err := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	if _, wrapped := v["value"]; wrapped && len(v) == 1 {
		return outcome{Value: normalize(v["value"])}
	}
	return outcome{Value: normalize(v)}
}

func restOutcome(t *testing.T, h http.Handler, method, path, token, body string) outcome {
	t.Helper()
	res := call(t, h, method, path, token, body)
	if res.status != 200 {
		var e struct {
			Error   string   `json:"error"`
			Message string   `json:"message"`
			Reasons []string `json:"reasons"`
		}
		_ = json.Unmarshal([]byte(res.raw), &e)
		return outcome{Code: e.Error, Message: e.Message, Reasons: e.Reasons}
	}
	var v any
	_ = json.Unmarshal([]byte(res.raw), &v)
	return outcome{Value: normalize(v)}
}

// normalize は null を除き、protojson が文字列にする int64 などの差を無くさない（この型には int64 が無い）。
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if e != nil {
				out[k] = normalize(e)
			}
		}
		return out
	case []any:
		for i := range x {
			x[i] = normalize(x[i])
		}
	}
	return v
}

// roadmap.md M2：REST と gRPC で、同じ入力に同じ出力・同じ拒否が返る。
func TestRESTAndGRPCAgree(t *testing.T) {
	srv := parityServer(t)
	clients := map[string]*grpcClient{"internal": dialGRPC(t, srv, "internal"), "public": dialGRPC(t, srv, "public")}
	full := `{"tags":["x","y"],"at":"2026-11-02T00:00:00Z","address":{"city":"Tokyo","zip":"100"},"count":3`
	cases := []struct {
		name, aud, token, id, query string
		body                        string // REST の本文（gRPC では id・dry を足す）
	}{
		{"internal ok", "internal", "internal", "t1", "?dry=true", full + `,"secret":"s"}`},
		{"public ok", "public", "user", "t1", "", full + `}`},
		{"no token", "public", "", "t1", "", full + `}`},
		{"user on internal", "internal", "user", "t1", "", full + `}`},
		{"denied", "public", "user-denied", "t1", "", full + `}`},
		{"denied with reasons", "internal", "internal", "t1", "", full + `}`},
		{"missing nested", "public", "user", "t1", "", `{"tags":[],"at":"2026-11-02T00:00:00Z"}`},
		// 必須のスカラーが無い場合は比べない。proto3 では無いこととゼロ値を区別できず、gRPC ではゼロ値として受ける（design.md 5.2）。
		{"not found", "internal", "internal", "missing", "", full + `}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rest := restOutcome(t, srv.Handler(c.aud), "PATCH", "/things/"+c.id+c.query, c.token, c.body)
			var in map[string]any
			_ = json.Unmarshal([]byte(c.body), &in)
			in["id"] = c.id
			if c.query != "" {
				in["dry"] = true
			}
			b, _ := json.Marshal(in)
			got := clients[c.aud].call(t, "Things", "Update", c.token, string(b))
			if !reflect.DeepEqual(rest, got) {
				t.Errorf("REST %+v\ngRPC %+v", rest, got)
			}
			if c.name == "internal ok" && (rest.Value == nil || rest.Value.(map[string]any)["secret"] != "s") {
				t.Errorf("internal output: %+v", rest)
			}
			if c.name == "public ok" && rest.Value.(map[string]any)["secret"] != nil {
				t.Errorf("secret leaked to public: %+v", rest)
			}
		})
	}
	// オブジェクトでない出力は value に包まれる。
	rest := restOutcome(t, srv.Handler("public"), "GET", "/things/names", "", "")
	got := clients["public"].call(t, "Things", "Names", "", `{}`)
	if !reflect.DeepEqual(rest, got) {
		t.Errorf("names: REST %+v gRPC %+v", rest, got)
	}
}

func TestGRPCServerOnlyForExposedAudiences(t *testing.T) {
	srv := parityServer(t)
	if _, err := srv.GRPCServer("partner"); err == nil {
		t.Fatal("partner exposes nothing over gRPC")
	}
}
