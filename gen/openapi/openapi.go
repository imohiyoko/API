// Package openapi は、マニフェストから公開範囲ごとの OpenAPI 3.1 を作る（design.md 5.2）。
// ある公開範囲の文書には、その公開範囲に REST で出す操作と、その公開範囲に見えるフィールドだけが載る。
package openapi

import (
	"encoding/json"
	"fmt"

	"github.com/imohiyoko/API/manifest"
)

// Info は文書の info。
type Info struct {
	Title   string
	Version string
}

type obj = map[string]any

// Generate は audience の OpenAPI 3.1 を JSON で返す。キーの順は決まっていて、同じ入力なら同じバイト列になる。
func Generate(m *manifest.Manifest, audience string, info Info) ([]byte, error) {
	if m.Rank(audience) < 0 {
		return nil, fmt.Errorf("openapi: unknown audience %q", audience)
	}
	if info.Title == "" {
		info.Title = "API"
	}
	if info.Version == "" {
		info.Version = "0.0.0"
	}
	visible := func(min string) bool { return m.Visible(min, audience) }
	paths := obj{}
	for i := range m.Operations {
		op := &m.Operations[i]
		if !op.Exposed(audience, manifest.REST) {
			continue
		}
		method, path, err := op.ResolveHTTP()
		if err != nil {
			return nil, fmt.Errorf("openapi: %s: %w", op.Name, err)
		}
		item, _ := paths[path].(obj)
		if item == nil {
			item = obj{}
			paths[path] = item
		}
		item[lower(method)] = operation(op, method, visible)
	}
	doc := obj{
		"openapi": "3.1.0",
		"info": obj{
			"title":       info.Title,
			"version":     info.Version,
			"description": fmt.Sprintf("Audience: %s. Generated from the API manifest; do not edit.", audience),
		},
		"paths": paths,
		"components": obj{
			"securitySchemes": obj{
				"bearer": obj{"type": "http", "scheme": "bearer"},
			},
			"schemas": obj{
				"Error": obj{
					"type":                 "object",
					"required":             []string{"error"},
					"additionalProperties": false,
					"properties": obj{
						"error":   obj{"type": "string"},
						"message": obj{"type": "string"},
						"reasons": obj{"type": "array", "items": obj{"type": "string"}},
					},
				},
			},
			"responses": obj{
				"Error": obj{
					"description": "Error",
					"content":     obj{"application/json": obj{"schema": obj{"$ref": "#/components/schemas/Error"}}},
				},
			},
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func operation(op *manifest.Operation, method string, visible func(string) bool) obj {
	o := obj{"operationId": op.Name}
	if op.Doc != "" {
		o["summary"] = op.Doc
	}
	var params []obj
	body := manifest.Schema{Type: "object"}
	for _, f := range op.Input.Fields {
		if !visible(f.Min) {
			continue
		}
		switch f.In {
		case "path", "query":
			params = append(params, obj{
				"name":     f.Name,
				"in":       f.In,
				"required": f.In == "path" || !f.Optional,
				"schema":   schema(f.Schema, visible),
			})
		case "body":
			body.Fields = append(body.Fields, f)
		}
	}
	if len(params) > 0 {
		o["parameters"] = params
	}
	if len(body.Fields) > 0 {
		o["requestBody"] = obj{
			"required": true,
			"content":  obj{"application/json": obj{"schema": schema(body, visible)}},
		}
	}
	errRef := obj{"$ref": "#/components/responses/Error"}
	responses := obj{
		"200": obj{
			"description": "OK",
			"content":     obj{"application/json": obj{"schema": schema(op.Output, visible)}},
		},
		"401": errRef,
		"403": errRef,
		"500": errRef,
		"503": errRef,
	}
	if len(op.Input.Fields) > 0 {
		responses["400"] = errRef
	}
	// ハンドラーが返す not_found や conflict など、操作ごとには分からない誤り。
	responses["default"] = errRef
	o["responses"] = responses
	if op.Anonymous {
		o["security"] = []obj{{}, {"bearer": []string{}}}
	} else {
		o["security"] = []obj{{"bearer": []string{}}}
	}
	if manifest.Idempotent(method) {
		o["x-idempotent"] = true
	}
	return o
}

func schema(s manifest.Schema, visible func(string) bool) obj {
	o := obj{"type": s.Type}
	if s.Format != "" {
		o["format"] = s.Format
	}
	switch s.Type {
	case "array":
		if s.Items != nil {
			o["items"] = schema(*s.Items, visible)
		}
	case "object":
		props := obj{}
		required := []string{}
		for _, f := range s.Fields {
			if !visible(f.Min) {
				continue
			}
			props[f.Name] = schema(f.Schema, visible)
			if !f.Optional {
				required = append(required, f.Name)
			}
		}
		o["properties"] = props
		o["additionalProperties"] = false
		if len(required) > 0 {
			o["required"] = required
		}
	}
	return o
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
