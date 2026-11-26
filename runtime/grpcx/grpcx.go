// Package grpcx は、runtime.Server の gRPC の口（design.md 5.2）。
// gRPC を使う利用者だけがこのパッケージを import し、grpc-go・protobuf をビルドに含める（design.md 2.3）。
package grpcx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/reflection"
	reflectionv1 "google.golang.org/grpc/reflection/grpc_reflection_v1"
	reflectionv1alpha "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/gen/proto"
	"github.com/imohiyoko/API/manifest"
	"github.com/imohiyoko/API/runtime"
)

// ErrorDomain は gRPC のエラーの ErrorInfo の domain。
const ErrorDomain = "github.com/imohiyoko/API"

// Server は公開範囲の gRPC の口を作る。生成コードを使わず、マニフェストから作った記述子でメッセージを組み立てる（design.md 5.2）。
// 生成した .proto（cli の gen）と同じ記述子なので、そのまま呼べる。gRPC で出す操作が無い公開範囲ならエラー。
// AudienceConfig.GRPCReflection なら、サーバーリフレクション（grpcurl など）も載せる。
// 呼び出しは srv.Serve を通るので、認証・認可・入力の検証・出力の絞り込みは REST と同じになる。
func Server(srv *runtime.Server, audience string, opts ...grpc.ServerOption) (*grpc.Server, error) {
	m := srv.Manifest()
	ac, ok := srv.AudienceConfig(audience)
	if !ok || !proto.Exposes(m, audience) {
		return nil, fmt.Errorf("grpcx: %s exposes nothing over gRPC", audience)
	}
	file, err := proto.File(m, audience)
	if err != nil {
		return nil, fmt.Errorf("grpcx: gRPC for %s: %w", audience, err)
	}
	ops := map[string]*manifest.Operation{} // サービス/メソッド → 操作
	pkg := proto.PackageName(m, audience)
	for i := range m.Operations {
		spec := &m.Operations[i]
		if !spec.Exposed(audience, manifest.GRPC) || !manifest.GRPCServes(spec.Kind) {
			continue
		}
		n := spec.ResolveGRPC()
		ops[pkg+"."+n.Service+"/"+n.Method] = spec
	}
	gs := grpc.NewServer(append([]grpc.ServerOption{grpc.MaxRecvMsgSize(runtime.MaxBody)}, opts...)...)
	svcs := file.Services()
	for i := 0; i < svcs.Len(); i++ {
		sd := svcs.Get(i)
		desc := grpc.ServiceDesc{ServiceName: string(sd.FullName()), HandlerType: (*any)(nil), Metadata: file.Path()}
		for j := 0; j < sd.Methods().Len(); j++ {
			md := sd.Methods().Get(j)
			op := &grpcOp{srv: srv, aud: audience, spec: ops[string(sd.FullName())+"/"+string(md.Name())], method: md}
			desc.Methods = append(desc.Methods, grpc.MethodDesc{MethodName: string(md.Name()), Handler: op.handle})
		}
		gs.RegisterService(&desc, struct{}{})
	}
	if ac.GRPCReflection {
		var files protoregistry.Files
		_ = files.RegisterFile(timestamppb.File_google_protobuf_timestamp_proto)
		if err := files.RegisterFile(file); err != nil {
			return nil, err
		}
		ro := reflection.ServerOptions{Services: gs, DescriptorResolver: &files, ExtensionResolver: protoregistry.GlobalTypes}
		reflectionv1.RegisterServerReflectionServer(gs, reflection.NewServerV1(ro))
		reflectionv1alpha.RegisterServerReflectionServer(gs, reflection.NewServer(ro))
	}
	return gs, nil
}

type grpcOp struct {
	srv    *runtime.Server
	aud    string
	spec   *manifest.Operation
	method protoreflect.MethodDescriptor
}

func (g *grpcOp) handle(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	req := dynamicpb.NewMessage(g.method.Input())
	if err := dec(req); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return g.serve(ctx, req)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/" + string(g.method.Parent().FullName()) + "/" + string(g.method.Name())}
	return interceptor(ctx, req, info, func(ctx context.Context, r any) (any, error) { return g.serve(ctx, r.(*dynamicpb.Message)) })
}

func (g *grpcOp) serve(ctx context.Context, req *dynamicpb.Message) (any, error) {
	r := runtime.Request{}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("authorization"); len(v) > 0 {
			r.Token, r.HasToken = runtime.Bearer(v[0])
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		r.Client = runtime.ClientHost(p.Addr.String())
	}
	// gRPC にはパスやクエリが無いので、すべてのフィールドがメッセージに入る。REST での場所への振り分けは Serve が行う。
	// 変換の誤りは、REST と同じく認証と主体の確認のあとで invalid_argument になる。
	r.Fields, r.Err = messageJSON(req)
	out, e := g.srv.Serve(ctx, g.aud, g.spec.Name, r)
	if e != nil {
		return nil, statusOf(e)
	}
	reply := dynamicpb.NewMessage(g.method.Output())
	if g.spec.Output.Type != "object" {
		out = map[string]any{proto.ValueField: out}
	}
	if err := fillMessage(reply, out); err != nil {
		g.srv.Logger().Error("api handler error", "operation", g.spec.Name, "error", err)
		return nil, statusOf(&define.Error{Code: define.CodeInternal})
	}
	return reply, nil
}

// grpcCodes は define のコードと gRPC のコードの対応。
var grpcCodes = map[define.Code]codes.Code{
	define.CodeInvalidArgument:    codes.InvalidArgument,
	define.CodeUnauthenticated:    codes.Unauthenticated,
	define.CodePermissionDenied:   codes.PermissionDenied,
	define.CodeNotFound:           codes.NotFound,
	define.CodeConflict:           codes.AlreadyExists,
	define.CodeFailedPrecondition: codes.FailedPrecondition,
	define.CodeResourceExhausted:  codes.ResourceExhausted,
	define.CodeUnavailable:        codes.Unavailable,
	define.CodeInternal:           codes.Internal,
}

// statusOf は、Serve が返したエラー（公開範囲の Disclosure に従って絞り込み済み）を gRPC の status にする。
// コードは ErrorInfo の reason、理由は metadata の reasons（カンマ区切り）に入れる。
func statusOf(e *define.Error) error {
	msg := e.Message
	if msg == "" {
		msg = string(e.Code)
	}
	code, ok := grpcCodes[e.Code]
	if !ok {
		code = codes.Unknown
	}
	info := &errdetails.ErrorInfo{Reason: string(e.Code), Domain: ErrorDomain}
	if len(e.Reasons) > 0 {
		info.Metadata = map[string]string{"reasons": strings.Join(e.Reasons, ",")}
	}
	details := []protoadapt.MessageV1{info}
	if e.RetryAfter > 0 {
		details = append(details, &errdetails.RetryInfo{RetryDelay: durationpb.New(e.RetryAfter)})
	}
	st, derr := status.New(code, msg).WithDetails(details...)
	if derr != nil {
		return status.New(code, msg).Err()
	}
	return st.Err()
}

// messageJSON はメッセージを、フィールドの名前 → JSON の値にする。値の形は REST の JSON と同じ
// （Timestamp は RFC 3339 の文字列、bytes は base64）。proto3 の optional でないスカラーは、ゼロ値でもあるものとする。
// 設定されていないメッセージと optional のフィールドは入れない。
func messageJSON(msg protoreflect.Message) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.HasPresence() && !msg.Has(fd) {
			continue
		}
		v, err := fieldJSON(fd, msg.Get(fd))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", fd.Name(), err)
		}
		out[string(fd.Name())] = v
	}
	return out, nil
}

func fieldJSON(fd protoreflect.FieldDescriptor, v protoreflect.Value) (json.RawMessage, error) {
	if fd.IsList() {
		l := v.List()
		items := make([]json.RawMessage, l.Len())
		for i := range items {
			r, err := valueJSON(fd, l.Get(i))
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			items[i] = r
		}
		return json.Marshal(items)
	}
	return valueJSON(fd, v)
}

func valueJSON(fd protoreflect.FieldDescriptor, v protoreflect.Value) (json.RawMessage, error) {
	switch fd.Kind() {
	case protoreflect.MessageKind:
		m := v.Message()
		if m.Descriptor().FullName() == "google.protobuf.Timestamp" {
			sec := m.Get(m.Descriptor().Fields().ByName("seconds")).Int()
			nanos := m.Get(m.Descriptor().Fields().ByName("nanos")).Int()
			return json.Marshal(time.Unix(sec, nanos).UTC().Format(time.RFC3339Nano))
		}
		obj, err := messageJSON(m)
		if err != nil {
			return nil, err
		}
		return json.Marshal(obj)
	case protoreflect.BytesKind:
		return json.Marshal(base64.StdEncoding.EncodeToString(v.Bytes()))
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		if f := v.Float(); math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("NaN and Inf are not allowed")
		}
	}
	return json.Marshal(v.Interface())
}

// fillMessage は define.Encode の結果をメッセージに入れる。nil のフィールドは設定しない。
func fillMessage(msg protoreflect.Message, value any) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("output must be an object, got %T", value)
	}
	fields := msg.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		v, ok := obj[string(fd.Name())]
		if !ok || v == nil {
			continue
		}
		if fd.IsList() {
			items, ok := v.([]any)
			if !ok {
				return fmt.Errorf("%s: want a list, got %T", fd.Name(), v)
			}
			l := msg.Mutable(fd).List()
			for _, item := range items {
				pv, err := protoValue(msg, fd, item, true)
				if err != nil {
					return fmt.Errorf("%s: %w", fd.Name(), err)
				}
				l.Append(pv)
			}
			continue
		}
		pv, err := protoValue(msg, fd, v, false)
		if err != nil {
			return fmt.Errorf("%s: %w", fd.Name(), err)
		}
		msg.Set(fd, pv)
	}
	return nil
}

func protoValue(parent protoreflect.Message, fd protoreflect.FieldDescriptor, v any, inList bool) (protoreflect.Value, error) {
	if fd.Kind() == protoreflect.MessageKind {
		var m protoreflect.Message
		if inList {
			m = parent.Mutable(fd).List().NewElement().Message()
		} else {
			m = parent.NewField(fd).Message()
		}
		if m.Descriptor().FullName() == "google.protobuf.Timestamp" {
			t, ok := v.(time.Time)
			if !ok {
				return protoreflect.Value{}, fmt.Errorf("want a time, got %T", v)
			}
			m.Set(m.Descriptor().Fields().ByName("seconds"), protoreflect.ValueOfInt64(t.Unix()))
			m.Set(m.Descriptor().Fields().ByName("nanos"), protoreflect.ValueOfInt32(int32(t.Nanosecond())))
			return protoreflect.ValueOfMessage(m), nil
		}
		if err := fillMessage(m, v); err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfMessage(m), nil
	}
	rv := reflect.ValueOf(v)
	switch fd.Kind() {
	case protoreflect.StringKind:
		if rv.Kind() == reflect.String {
			return protoreflect.ValueOfString(rv.String()), nil
		}
	case protoreflect.BoolKind:
		if rv.Kind() == reflect.Bool {
			return protoreflect.ValueOfBool(rv.Bool()), nil
		}
	case protoreflect.BytesKind:
		if b, ok := v.([]byte); ok {
			return protoreflect.ValueOfBytes(b), nil
		}
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		var n int64
		switch {
		case rv.CanInt():
			n = rv.Int()
		case rv.CanUint() && rv.Uint() <= math.MaxInt64:
			n = int64(rv.Uint())
		default:
			return protoreflect.Value{}, fmt.Errorf("want an integer, got %T", v)
		}
		if fd.Kind() == protoreflect.Int32Kind {
			if n < math.MinInt32 || n > math.MaxInt32 {
				return protoreflect.Value{}, fmt.Errorf("%d does not fit in int32", n)
			}
			return protoreflect.ValueOfInt32(int32(n)), nil
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.FloatKind:
		if rv.CanFloat() {
			return protoreflect.ValueOfFloat32(float32(rv.Float())), nil
		}
	case protoreflect.DoubleKind:
		if rv.CanFloat() {
			return protoreflect.ValueOfFloat64(rv.Float()), nil
		}
	}
	return protoreflect.Value{}, fmt.Errorf("cannot put %T into a %s field", v, fd.Kind())
}
