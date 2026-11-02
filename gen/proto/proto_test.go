package proto_test

import (
	"strings"
	"testing"

	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/gen/proto"
	"github.com/imohiyoko/API/manifest"
)

func usersManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := users.Registry(&users.Store{}).Manifest()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestGenerateInternal(t *testing.T) {
	m := usersManifest(t)
	b, err := proto.Generate(m, manifest.Internal)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	t.Log(s)
	for _, want := range []string{
		"package users.internal;",
		"service Users {",
		"rpc Get(UsersGetRequest) returns (UsersGetResponse);",
		"string note = 4;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	again, _ := proto.Generate(m, manifest.Internal)
	if string(again) != s {
		t.Error("not deterministic")
	}
}

func TestOnlyExposedAudiences(t *testing.T) {
	m := usersManifest(t)
	if !proto.Exposes(m, manifest.Internal) || proto.Exposes(m, manifest.Public) {
		t.Fatal("users exposes gRPC only on internal")
	}
}

func TestFieldsKeepNumbersAndVisibility(t *testing.T) {
	str := manifest.Schema{Type: "string"}
	m := &manifest.Manifest{Version: manifest.Version, Name: "acme", Operations: []manifest.Operation{{
		Name: "things.get", Kind: manifest.KindQuery, Anonymous: true,
		Input: manifest.Schema{Type: "object", Fields: []manifest.Field{
			{Name: "id", In: "path", Schema: str},
			{Name: "limit", In: "query", Optional: true, Schema: manifest.Schema{Type: "integer", Format: "int32"}},
		}},
		Output: manifest.Schema{Type: "object", Fields: []manifest.Field{
			{Name: "secret", Min: manifest.Internal, Schema: str},
			{Name: "created_at", Schema: manifest.Schema{Type: "string", Format: "date-time"}},
			{Name: "tags", Schema: manifest.Schema{Type: "array", Items: &manifest.Schema{Type: "object", Fields: []manifest.Field{{Name: "k", Schema: str}}}}},
		}},
		Expose: []manifest.Exposure{{Audience: manifest.Public, Protocols: []string{manifest.GRPC}}},
	}}}
	b, err := proto.Generate(m, manifest.Public)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"package acme.public;",
		`import "google/protobuf/timestamp.proto";`,
		"optional int32 limit = 2;",
		"google.protobuf.Timestamp created_at = 2 [json_name = \"created_at\"];",
		"repeated Tags tags = 3;",
		"message Tags {",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "secret") {
		t.Error("internal field leaked to public")
	}
	if _, err := proto.File(m, manifest.Public); err != nil {
		t.Fatal(err)
	}
}

func TestNonObjectOutputIsWrapped(t *testing.T) {
	m := &manifest.Manifest{Version: manifest.Version, Operations: []manifest.Operation{{
		Name: "ping", Kind: manifest.KindQuery, Anonymous: true,
		Input:  manifest.Schema{Type: "object"},
		Output: manifest.Schema{Type: "array", Items: &manifest.Schema{Type: "string"}},
		Expose: []manifest.Exposure{{Audience: manifest.Public, Protocols: []string{manifest.GRPC}}},
	}}}
	b, err := proto.Generate(m, manifest.Public)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, "service Default {") || !strings.Contains(s, "repeated string value = 1;") {
		t.Fatal(s)
	}
}
