package manifest

import (
	"fmt"
	"regexp"
	"strings"
)

// GRPCNames は gRPC でのサービス名・メソッド名と、要求・応答のメッセージの名前の元（design.md 5.1）。
type GRPCNames struct {
	Service string // 例：users.get → Users。語が1つなら Default
	Method  string // 例：users.get → Get
	Request string // 例：UsersGetRequest
	Reply   string // 例：UsersGetResponse
}

// ResolveGRPC は操作の gRPC での名前を返す。名前の最後の語がメソッド、それより前の語がサービスになる。
func (o *Operation) ResolveGRPC() GRPCNames {
	words := strings.Split(o.Name, ".")
	svc := "Default"
	if len(words) > 1 {
		svc = ""
		for _, w := range words[:len(words)-1] {
			svc += Pascal(w)
		}
	}
	base := ""
	for _, w := range words {
		base += Pascal(w)
	}
	return GRPCNames{Service: svc, Method: Pascal(words[len(words)-1]), Request: base + "Request", Reply: base + "Response"}
}

// Pascal は snake_case を PascalCase にする（例：user_id → UserId）。
func Pascal(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
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

// GRPCServes は、この版の gRPC で出せる種類か。Stream と Event はまだ（roadmap.md）。
func GRPCServes(kind string) bool { return kind == KindQuery || kind == KindCommand }

var protoIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// checkGRPC は gRPC で出すときの制約を見る。names は公開範囲ごとの、使った名前と操作。
type grpcIdent struct{ op, kind string }

func checkGRPC(op *Operation, aud string, names map[string]grpcIdent, add func(Severity, string, string, ...any)) {
	n := op.ResolveGRPC()
	// パッケージの直下の名前（サービスとメッセージ）と、メソッドの完全な名前がぶつからないこと。
	// サービスは複数の操作で同じでよいが、メッセージと同じ名前にはできない。
	idents := []struct{ name, kind string }{
		{n.Service, "service"}, {n.Service + "." + n.Method, "method"}, {n.Request, "message"}, {n.Reply, "message"},
	}
	for _, id := range idents {
		key := aud + " " + id.name
		prev, ok := names[key]
		if ok && prev.op != op.Name && !(id.kind == "service" && prev.kind == "service") {
			add(Error, op.Name, "gRPC name %s on %s conflicts with %s", id.name, aud, prev.op)
		}
		if !ok {
			names[key] = grpcIdent{op: op.Name, kind: id.kind}
		}
	}
	checkProtoSchema(op.Name, "input", op.Input, add)
	checkProtoSchema(op.Name, "output", op.Output, add)
}

func checkProtoSchema(op, where string, s Schema, add func(Severity, string, string, ...any)) {
	switch s.Type {
	case "array":
		if s.Items != nil && s.Items.Type == "array" {
			add(Error, op, "gRPC: %s cannot be an array of arrays", where)
			return
		}
		if s.Items != nil {
			checkProtoSchema(op, where, *s.Items, add)
		}
	case "object":
		nested := map[string]string{}
		for _, f := range s.Fields {
			p := where + "." + f.Name
			if !protoIdentRe.MatchString(f.Name) {
				add(Error, op, "gRPC: field %s is not a valid protobuf name", p)
			}
			if t := f.Schema.Type; t == "object" || (t == "array" && f.Schema.Items != nil && f.Schema.Items.Type == "object") {
				name := Pascal(f.Name)
				if other, ok := nested[name]; ok {
					add(Error, op, "gRPC: fields %s and %s map to the same message %s", other, f.Name, name)
				}
				nested[name] = f.Name
			}
			checkProtoSchema(op, p, f.Schema, add)
		}
	}
}

// grpcIgnored は、gRPC の公開を無視するときの警告の文。
func grpcIgnored(kind, aud string) string {
	return fmt.Sprintf("gRPC for a %s is not implemented yet; the %s exposure is ignored", kind, aud)
}
