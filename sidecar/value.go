package sidecar

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/manifest"
)

// 値の表し方は define.Encode の結果と同じにする（runtime の REST と gRPC がそのまま使える）：
// object は map[string]any、array は []any、string は string（date-time は time.Time、byte は []byte）、
// integer は int64、number は float64、boolean は bool、null は nil。

func invalid(format string, args ...any) *define.Error {
	return define.Errorf(define.CodeInvalidArgument, format, args...)
}

// decodeInput は、マニフェストの入力の型に従って要求から入力を組み立てる（define の Decode と同じ規則）。
func decodeInput(s manifest.Schema, src define.InputSource) (map[string]any, error) {
	body, err := src.Body()
	if err != nil {
		return nil, invalid("body: %v", err)
	}
	in := map[string]any{}
	known := map[string]bool{}
	for _, f := range s.Fields {
		visible := src.Visible(f.Min)
		if f.In == "body" && visible {
			known[f.Name] = true
		}
		var present bool
		switch f.In {
		case "path":
			raw := src.PathValue(f.Name)
			present = raw != ""
			if present && visible {
				v, err := parseScalar(f.Schema, raw)
				if err != nil {
					return nil, invalid("%s: %v", f.Name, err)
				}
				in[f.Name] = v
			}
		case "query":
			vals, ok := src.QueryValues(f.Name)
			present = ok && len(vals) > 0
			if present && !visible {
				return nil, invalid("unknown query parameter %q", f.Name)
			}
			if present {
				if len(vals) > 1 {
					return nil, invalid("%s: given more than once", f.Name)
				}
				v, err := parseScalar(f.Schema, vals[0])
				if err != nil {
					return nil, invalid("%s: %v", f.Name, err)
				}
				in[f.Name] = v
			}
		case "body":
			raw, ok := body[f.Name]
			present = ok && visible
			if present {
				v, err := decodeValue(raw, f.Schema, f.Optional, src.Visible)
				if err != nil {
					return nil, invalid("%s: %v", f.Name, err)
				}
				in[f.Name] = v
			}
		}
		if !present && visible && !f.Optional {
			return nil, invalid("%s is required", f.Name)
		}
	}
	for k := range body {
		if !known[k] {
			return nil, invalid("unknown field %q", k)
		}
	}
	return in, nil
}

// parseScalar はパスとクエリの文字列を型に合わせて読む。
func parseScalar(s manifest.Schema, raw string) (any, error) {
	switch s.Type {
	case "string":
		switch s.Format {
		case "date-time":
			return time.Parse(time.RFC3339, raw)
		case "byte":
			return nil, fmt.Errorf("type []uint8 cannot be given as text")
		}
		return raw, nil
	case "boolean":
		return strconv.ParseBool(raw)
	case "integer":
		bits := 64
		if s.Format == "int32" {
			bits = 32
		}
		return strconv.ParseInt(raw, 10, bits)
	case "number":
		bits := 64
		if s.Format == "float" {
			bits = 32
		}
		return strconv.ParseFloat(raw, bits)
	}
	return nil, fmt.Errorf("type %s cannot be given as text", s.Type)
}

// decodeValue は JSON の値を型に合わせて読む。見えないフィールドは無いものとして扱い（送られれば unknown field）、
// null は optional のフィールドだけ受ける。
func decodeValue(raw json.RawMessage, s manifest.Schema, optional bool, visible func(string) bool) (any, error) {
	if string(bytes.TrimSpace(raw)) == "null" {
		if !optional {
			return nil, fmt.Errorf("null is not allowed")
		}
		return nil, nil
	}
	switch s.Type {
	case "object":
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
		out := map[string]any{}
		for _, f := range s.Fields {
			if !visible(f.Min) {
				continue
			}
			r, ok := obj[f.Name]
			if !ok {
				if !f.Optional {
					return nil, fmt.Errorf("%s is required", f.Name)
				}
				continue
			}
			v, err := decodeValue(r, f.Schema, f.Optional, visible)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			out[f.Name] = v
			delete(obj, f.Name)
		}
		for k := range obj {
			return nil, fmt.Errorf("unknown field %q", k)
		}
		return out, nil
	case "array":
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		if s.Items == nil {
			return nil, fmt.Errorf("array without items")
		}
		out := make([]any, len(arr))
		for i, r := range arr {
			v, err := decodeValue(r, *s.Items, false, visible)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = v
		}
		return out, nil
	}
	return scalarJSON(raw, s)
}

func scalarJSON(raw json.RawMessage, s manifest.Schema) (any, error) {
	switch s.Type {
	case "string":
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return nil, err
		}
		switch s.Format {
		case "date-time":
			return time.Parse(time.RFC3339, str)
		case "byte":
			return base64.StdEncoding.DecodeString(str)
		}
		return str, nil
	case "boolean":
		var b bool
		err := json.Unmarshal(raw, &b)
		return b, err
	case "integer":
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, err
		}
		if s.Format == "int32" && (n < math.MinInt32 || n > math.MaxInt32) {
			return nil, fmt.Errorf("%d does not fit in int32", n)
		}
		return n, nil
	case "number":
		var f float64
		err := json.Unmarshal(raw, &f)
		return f, err
	}
	return nil, fmt.Errorf("unknown type %q", s.Type)
}

// output は本体が返した出力。公開範囲に見えるフィールドだけを、型を確かめて取り出す（define.Encoder）。
// 本体が余計なフィールドや見せないフィールドを返しても、呼び出し元には出さない。
type output struct {
	raw    json.RawMessage
	schema manifest.Schema
}

// EncodeVisible は define.Encoder の実装。
func (o output) EncodeVisible(visible func(min string) bool) (any, error) {
	v, err := encodeValue(o.raw, o.schema, true, visible)
	if err != nil {
		return nil, fmt.Errorf("sidecar: output from the upstream: %w", err)
	}
	return v, nil
}

func encodeValue(raw json.RawMessage, s manifest.Schema, nullable bool, visible func(string) bool) (any, error) {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		if !nullable {
			return nil, fmt.Errorf("null is not allowed")
		}
		return nil, nil
	}
	switch s.Type {
	case "object":
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(s.Fields))
		for _, f := range s.Fields {
			if !visible(f.Min) {
				continue
			}
			r, ok := obj[f.Name]
			if !ok && !f.Optional {
				return nil, fmt.Errorf("%s is required", f.Name)
			}
			v, err := encodeValue(r, f.Schema, f.Optional, visible)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			out[f.Name] = v
		}
		return out, nil
	case "array":
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, err
		}
		if s.Items == nil {
			return nil, fmt.Errorf("array without items")
		}
		out := make([]any, len(arr))
		for i, r := range arr {
			v, err := encodeValue(r, *s.Items, false, visible)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = v
		}
		return out, nil
	}
	return scalarJSON(raw, s)
}
