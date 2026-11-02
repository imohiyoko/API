// Package proto は、マニフェストから公開範囲ごとの .proto を作る（design.md 5.2）。
// 同じ記述子を runtime の gRPC サーバーも使うので、生成した .proto と実際の口は食い違わない。
package proto

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/imohiyoko/API/manifest"
)

const timestampFile = "google/protobuf/timestamp.proto"

// ValueField は、出力がオブジェクトでないときに応答を包むフィールドの名前。
const ValueField = "value"

// Exposes は、その公開範囲に gRPC で出す操作があるか。
func Exposes(m *manifest.Manifest, audience string) bool {
	return slices.ContainsFunc(m.Operations, func(op manifest.Operation) bool { return serves(&op, audience) })
}

func serves(op *manifest.Operation, audience string) bool {
	return op.Exposed(audience, manifest.GRPC) && manifest.GRPCServes(op.Kind)
}

// PackageName は公開範囲の protobuf のパッケージ名。API の名前に公開範囲の名前を足す（例：users.internal）。
func PackageName(m *manifest.Manifest, audience string) string {
	return m.APIName() + "." + audience
}

// Descriptor は公開範囲の .proto の記述子を作る。フィールドの番号は、定義の中のフィールドの順番（1から）で、
// 公開範囲で見えないフィールドを除いても変わらない。定義のフィールドの順番を変えると、互換性が壊れる。
func Descriptor(m *manifest.Manifest, audience string) (*descriptorpb.FileDescriptorProto, error) {
	pkg := PackageName(m, audience)
	b := &builder{m: m, aud: audience, pkg: pkg}
	fd := &descriptorpb.FileDescriptorProto{
		Name:    ptr(strings.ReplaceAll(pkg, ".", "/") + ".proto"),
		Package: ptr(pkg),
		Syntax:  ptr("proto3"),
	}
	services := map[string]*descriptorpb.ServiceDescriptorProto{}
	for i := range m.Operations {
		op := &m.Operations[i]
		if !serves(op, audience) {
			continue
		}
		n := op.ResolveGRPC()
		req, err := b.message(n.Request, "."+pkg, op.Input)
		if err != nil {
			return nil, fmt.Errorf("%s: input: %w", op.Name, err)
		}
		out := op.Output
		if out.Type != "object" {
			// gRPC の応答はメッセージなので、オブジェクトでない出力は value に包む。
			out = manifest.Schema{Type: "object", Fields: []manifest.Field{{Name: ValueField, Schema: op.Output}}}
		}
		rep, err := b.message(n.Reply, "."+pkg, out)
		if err != nil {
			return nil, fmt.Errorf("%s: output: %w", op.Name, err)
		}
		fd.MessageType = append(fd.MessageType, req, rep)
		svc, ok := services[n.Service]
		if !ok {
			svc = &descriptorpb.ServiceDescriptorProto{Name: ptr(n.Service)}
			services[n.Service] = svc
			fd.Service = append(fd.Service, svc)
		}
		svc.Method = append(svc.Method, &descriptorpb.MethodDescriptorProto{
			Name:       ptr(n.Method),
			InputType:  ptr("." + pkg + "." + n.Request),
			OutputType: ptr("." + pkg + "." + n.Reply),
		})
	}
	if b.timestamp {
		fd.Dependency = []string{timestampFile}
	}
	return fd, nil
}

// File は記述子を検証して、protoreflect の記述子にする。runtime の gRPC サーバーが使う。
func File(m *manifest.Manifest, audience string) (protoreflect.FileDescriptor, error) {
	fdp, err := Descriptor(m, audience)
	if err != nil {
		return nil, err
	}
	return protodesc.NewFile(fdp, resolver())
}

// resolver は well-known types（Timestamp）を解く。
func resolver() *protoregistry.Files {
	var files protoregistry.Files
	_ = files.RegisterFile(timestamppb.File_google_protobuf_timestamp_proto)
	return &files
}

type builder struct {
	m         *manifest.Manifest
	aud       string
	pkg       string
	timestamp bool
}

func (b *builder) message(name, scope string, s manifest.Schema) (*descriptorpb.DescriptorProto, error) {
	msg := &descriptorpb.DescriptorProto{Name: ptr(name)}
	full := scope + "." + name
	nested := map[string]bool{}
	for i, f := range s.Fields {
		if !b.m.Visible(f.Min, b.aud) {
			continue
		}
		fp := &descriptorpb.FieldDescriptorProto{
			Name:     ptr(f.Name),
			JsonName: ptr(f.Name), // JSON（grpc-gateway や grpcurl）でも REST と同じ名前にする
			Number:   ptr(int32(i + 1)),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		}
		fs := f.Schema
		if fs.Type == "array" {
			if fs.Items == nil {
				return nil, fmt.Errorf("%s: array without items", f.Name)
			}
			fp.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
			fs = *fs.Items
		}
		switch fs.Type {
		case "object":
			nn := manifest.Pascal(f.Name)
			if nested[nn] {
				return nil, fmt.Errorf("%s: nested message %s is defined twice", f.Name, nn)
			}
			nested[nn] = true
			sub, err := b.message(nn, full, fs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			msg.NestedType = append(msg.NestedType, sub)
			fp.Type = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
			fp.TypeName = ptr(full + "." + nn)
		case "array":
			return nil, fmt.Errorf("%s: arrays of arrays are not supported", f.Name)
		default:
			t, err := b.scalar(fs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			if t == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
				fp.TypeName = ptr(".google.protobuf.Timestamp")
			}
			fp.Type = t.Enum()
			if f.Optional && fp.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED && t != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
				// proto3 の optional（有無を区別する）。合成した oneof に入れる。
				fp.Proto3Optional = ptr(true)
				fp.OneofIndex = ptr(int32(len(msg.OneofDecl)))
				msg.OneofDecl = append(msg.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: ptr("_" + f.Name)})
			}
		}
		msg.Field = append(msg.Field, fp)
	}
	return msg, nil
}

func (b *builder) scalar(s manifest.Schema) (descriptorpb.FieldDescriptorProto_Type, error) {
	switch s.Type {
	case "string":
		switch s.Format {
		case "date-time":
			b.timestamp = true
			return descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, nil
		case "byte":
			return descriptorpb.FieldDescriptorProto_TYPE_BYTES, nil
		}
		return descriptorpb.FieldDescriptorProto_TYPE_STRING, nil
	case "boolean":
		return descriptorpb.FieldDescriptorProto_TYPE_BOOL, nil
	case "integer":
		if s.Format == "int32" {
			return descriptorpb.FieldDescriptorProto_TYPE_INT32, nil
		}
		return descriptorpb.FieldDescriptorProto_TYPE_INT64, nil
	case "number":
		if s.Format == "float" {
			return descriptorpb.FieldDescriptorProto_TYPE_FLOAT, nil
		}
		return descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, nil
	}
	return 0, fmt.Errorf("type %q is not supported", s.Type)
}

func ptr[T any](v T) *T { return &v }

// Generate は公開範囲の .proto の本文を作る。同じ定義からは同じ本文になる。
func Generate(m *manifest.Manifest, audience string) ([]byte, error) {
	fd, err := Descriptor(m, audience)
	if err != nil {
		return nil, err
	}
	if _, err := protodesc.NewFile(fd, resolver()); err != nil {
		return nil, err
	}
	docs := map[string]string{}
	for _, op := range m.Operations {
		n := op.ResolveGRPC()
		docs[n.Service+"."+n.Method] = op.Doc
	}
	var w strings.Builder
	fmt.Fprintf(&w, "// Code generated from the API manifest; DO NOT EDIT.\n// Audience: %s.\n\nsyntax = \"proto3\";\n\npackage %s;\n", audience, fd.GetPackage())
	if len(fd.Dependency) > 0 {
		w.WriteString("\n")
		deps := slices.Clone(fd.Dependency)
		sort.Strings(deps)
		for _, d := range deps {
			fmt.Fprintf(&w, "import %q;\n", d)
		}
	}
	for _, s := range fd.Service {
		fmt.Fprintf(&w, "\nservice %s {\n", s.GetName())
		for _, mth := range s.Method {
			if d := docs[s.GetName()+"."+mth.GetName()]; d != "" {
				writeComment(&w, "  ", d)
			}
			fmt.Fprintf(&w, "  rpc %s(%s) returns (%s);\n", mth.GetName(), relName(mth.GetInputType(), "."+fd.GetPackage()), relName(mth.GetOutputType(), "."+fd.GetPackage()))
		}
		w.WriteString("}\n")
	}
	for _, msg := range fd.MessageType {
		w.WriteString("\n")
		writeMessage(&w, msg, "", "."+fd.GetPackage())
	}
	return []byte(w.String()), nil
}

func writeComment(w *strings.Builder, indent, text string) {
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(w, "%s// %s\n", indent, line)
	}
}

func writeMessage(w *strings.Builder, msg *descriptorpb.DescriptorProto, indent, scope string) {
	fmt.Fprintf(w, "%smessage %s {\n", indent, msg.GetName())
	inner := scope + "." + msg.GetName()
	for _, f := range msg.Field {
		label := ""
		switch {
		case f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED:
			label = "repeated "
		case f.GetProto3Optional():
			label = "optional "
		}
		typ := scalarName[f.GetType()]
		if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
			typ = relName(f.GetTypeName(), inner)
		}
		opt := ""
		if f.GetJsonName() != defaultJSONName(f.GetName()) {
			opt = fmt.Sprintf(" [json_name = %q]", f.GetJsonName())
		}
		fmt.Fprintf(w, "%s  %s%s %s = %d%s;\n", indent, label, typ, f.GetName(), f.GetNumber(), opt)
	}
	for _, n := range msg.NestedType {
		w.WriteString("\n")
		writeMessage(w, n, indent+"  ", inner)
	}
	fmt.Fprintf(w, "%s}\n", indent)
}

// relName は、型の完全な名前を scope からの相対の名前にする。
func relName(full, scope string) string {
	if full == ".google.protobuf.Timestamp" {
		return "google.protobuf.Timestamp"
	}
	if strings.HasPrefix(full, scope+".") {
		return strings.TrimPrefix(full, scope+".")
	}
	return full
}

// defaultJSONName は protoc が決める JSON の名前（user_id → userId）。
func defaultJSONName(name string) string {
	var b strings.Builder
	up := false
	for _, r := range name {
		if r == '_' {
			up = true
			continue
		}
		if up && 'a' <= r && r <= 'z' {
			r -= 'a' - 'A'
		}
		up = false
		b.WriteRune(r)
	}
	return b.String()
}

var scalarName = map[descriptorpb.FieldDescriptorProto_Type]string{
	descriptorpb.FieldDescriptorProto_TYPE_STRING: "string",
	descriptorpb.FieldDescriptorProto_TYPE_BYTES:  "bytes",
	descriptorpb.FieldDescriptorProto_TYPE_BOOL:   "bool",
	descriptorpb.FieldDescriptorProto_TYPE_INT32:  "int32",
	descriptorpb.FieldDescriptorProto_TYPE_INT64:  "int64",
	descriptorpb.FieldDescriptorProto_TYPE_FLOAT:  "float",
	descriptorpb.FieldDescriptorProto_TYPE_DOUBLE: "double",
}
