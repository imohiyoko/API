package iam

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/imohiyoko/API/manifest"
)

// iam の権限の名前の形（iam の ADR 0121）。
var (
	catalogPrefixRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// actionRE は接頭辞の後ろ（<リソース>:<操作>）の形。操作の `*` はロールに入れる書き方で、カタログには登録できず、
	// 1つの操作が求める権限にもならないので受けない。
	actionRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}:[a-z0-9_.-]{1,64}$`)
)

// ValidCatalogPrefix は iam の権限のカタログの接頭辞の形か。
func ValidCatalogPrefix(p string) bool { return catalogPrefixRE.MatchString(p) }

// Permission は、操作の Authz を iam の連携先の権限の名前（<接頭辞>.<リソース>:<操作>）にする。
func Permission(prefix, action string) string { return prefix + "." + action }

// CatalogPermission は iam の権限のカタログの1件（PermissionDef）。
type CatalogPermission struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Catalog は iam の POST /v1/admin/permission-catalogs の本体（PermissionCatalogCreate）。
type Catalog struct {
	Prefix      string              `json:"prefix"`
	DisplayName string              `json:"display_name"`
	Permissions []CatalogPermission `json:"permissions"`
}

// maxCatalogPermissions は iam の1つのカタログの権限の上限。
const maxCatalogPermissions = 500

// CatalogFromManifest は、マニフェストの操作の Authz から iam の権限のカタログを作る。説明はその権限を求める操作の名前。
// displayName が空ならマニフェストの API の名前（64 文字まで）。iam の権限の形に合わない Authz があればエラー。
func CatalogFromManifest(m *manifest.Manifest, prefix, displayName string) (Catalog, error) {
	if !ValidCatalogPrefix(prefix) {
		return Catalog{}, fmt.Errorf("iam: catalog prefix %q: use 1-32 lowercase letters, digits and -, starting with a letter", prefix)
	}
	if displayName == "" {
		displayName = m.APIName()
	}
	if len(displayName) > 64 {
		return Catalog{}, fmt.Errorf("iam: catalog display name %q: at most 64 characters", displayName)
	}
	ops := map[string][]string{}
	for i := range m.Operations {
		op := &m.Operations[i]
		if op.Authz == nil {
			continue
		}
		if !actionRE.MatchString(op.Authz.Action) {
			return Catalog{}, fmt.Errorf("iam: %s: authz action %q is not an iam permission (<resource>:<action>, lowercase)", op.Name, op.Authz.Action)
		}
		ops[op.Authz.Action] = append(ops[op.Authz.Action], op.Name)
	}
	if len(ops) > maxCatalogPermissions {
		return Catalog{}, fmt.Errorf("iam: %d permissions: an iam catalog holds at most %d", len(ops), maxCatalogPermissions)
	}
	c := Catalog{Prefix: prefix, DisplayName: displayName, Permissions: []CatalogPermission{}}
	for _, a := range slices.Sorted(maps.Keys(ops)) {
		desc := strings.Join(ops[a], ", ")
		if len(desc) > 256 {
			desc = desc[:253] + "..."
		}
		c.Permissions = append(c.Permissions, CatalogPermission{Name: Permission(prefix, a), Description: desc})
	}
	return c, nil
}

// MarshalCatalog はカタログを JSON にする。
func MarshalCatalog(c Catalog) ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
