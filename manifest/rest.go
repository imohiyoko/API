package manifest

import (
	"fmt"
	"strings"
)

// commandMethods は、Command の名前の最後の語から決める HTTP メソッド（design.md 5.1）。
var commandMethods = map[string]string{
	"create":  "POST",
	"replace": "PUT",
	"update":  "PATCH",
	"delete":  "DELETE",
}

// queryVerbs は、Query の名前の最後の語のうち、既定のパスから落とす語。
var queryVerbs = map[string]bool{"get": true, "list": true}

// ResolveHTTP は REST での HTTP メソッドとパスを返す。
//
// メソッド：Query は GET。Command は HTTP.Method、無ければ名前の最後の語（create・replace・update・delete）で決める。
// パス：HTTP.Path、無ければ名前の語を / でつなぎ（最後の語が get・list・create・replace・update・delete なら落とす）、
// path のフィールドを `/{name}` で足す。例：users.get（入力 id が path）→ GET /users/{id}。
func (o *Operation) ResolveHTTP() (method, path string, err error) {
	words := strings.Split(o.Name, ".")
	last := words[len(words)-1]
	if o.HTTP != nil && o.HTTP.Method != "" {
		method = strings.ToUpper(o.HTTP.Method)
	}
	switch o.Kind {
	case KindQuery:
		if method == "" {
			method = "GET"
		}
		if method != "GET" {
			return "", "", fmt.Errorf("a query must use GET, not %s", method)
		}
	case KindCommand:
		if method == "" {
			method = commandMethods[last]
		}
		if method == "" {
			return "", "", fmt.Errorf("cannot decide the HTTP method from %q: name it *.create/replace/update/delete or set define.HTTP", o.Name)
		}
		if method == "GET" {
			return "", "", fmt.Errorf("a command must not use GET")
		}
	default:
		return "", "", fmt.Errorf("kind %q has no REST request/response form", o.Kind)
	}
	if o.HTTP != nil && o.HTTP.Path != "" {
		return method, o.HTTP.Path, nil
	}
	if len(words) > 1 && (queryVerbs[last] || commandMethods[last] != "") {
		words = words[:len(words)-1]
	}
	var b strings.Builder
	for _, w := range words {
		b.WriteString("/" + w)
	}
	for _, f := range o.Input.Fields {
		if f.In == "path" {
			b.WriteString("/{" + f.Name + "}")
		}
	}
	return method, b.String(), nil
}

// PathParams はパスの `{name}` を順に返す。
func PathParams(path string) []string {
	var out []string
	for {
		i := strings.IndexByte(path, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(path[i:], '}')
		if j < 0 {
			return out
		}
		out = append(out, path[i+1:i+j])
		path = path[i+j+1:]
	}
}

// RouteKey はパスの引数の名前を除いた、衝突を調べるためのキー。
func RouteKey(method, path string) string {
	var b strings.Builder
	b.WriteString(method + " ")
	for {
		i := strings.IndexByte(path, '{')
		if i < 0 {
			b.WriteString(path)
			return b.String()
		}
		j := strings.IndexByte(path[i:], '}')
		if j < 0 {
			b.WriteString(path)
			return b.String()
		}
		b.WriteString(path[:i] + "{}")
		path = path[i+j+1:]
	}
}

// Idempotent は契約に冪等と書くメソッドか（design.md 5.1。PUT・DELETE と、安全な GET）。
func Idempotent(method string) bool {
	return method == "GET" || method == "PUT" || method == "DELETE"
}
