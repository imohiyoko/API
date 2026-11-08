package iam_test

import (
	"strings"
	"testing"

	"github.com/imohiyoko/API/adapters/iam"
	"github.com/imohiyoko/API/manifest"
)

func TestCatalogFromManifest(t *testing.T) {
	m := &manifest.Manifest{Name: "users", Operations: []manifest.Operation{
		{Name: "users.get", Authz: &manifest.Authz{Action: "users:read"}},
		{Name: "users.search", Authz: &manifest.Authz{Action: "users:read"}},
		{Name: "users.delete", Authz: &manifest.Authz{Action: "users:delete"}},
		{Name: "health", Anonymous: true},
	}}
	c, err := iam.CatalogFromManifest(m, "users-api", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Prefix != "users-api" || c.DisplayName != "users" || len(c.Permissions) != 2 ||
		c.Permissions[0] != (iam.CatalogPermission{Name: "users-api.users:delete", Description: "users.delete"}) ||
		c.Permissions[1] != (iam.CatalogPermission{Name: "users-api.users:read", Description: "users.get, users.search"}) {
		t.Fatalf("got %+v", c)
	}
	b, err := iam.MarshalCatalog(c)
	if err != nil || !strings.Contains(string(b), `"display_name": "users"`) {
		t.Fatalf("got %s %v", b, err)
	}

	if _, err := iam.CatalogFromManifest(m, "Users", ""); err == nil {
		t.Error("bad prefix: want an error")
	}
	for _, a := range []string{"Users:Read", "users:*", "users"} {
		bad := &manifest.Manifest{Operations: []manifest.Operation{{Name: "x.y", Authz: &manifest.Authz{Action: a}}}}
		if _, err := iam.CatalogFromManifest(bad, "x", ""); err == nil || !strings.Contains(err.Error(), "x.y") {
			t.Errorf("%q: %v", a, err)
		}
	}
}
