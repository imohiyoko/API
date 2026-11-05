package define

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/imohiyoko/API/manifest"
)

// InputSource は、プロトコルが入力の値を渡す口。
type InputSource interface {
	// PathValue はパスの引数。無ければ空。
	PathValue(name string) string
	// QueryValues はクエリの値。
	QueryValues(name string) ([]string, bool)
	// Body は本文の JSON のオブジェクト。本文が無ければ nil。
	Body() (map[string]json.RawMessage, error)
	// Visible は、min を付けたフィールドがこの公開範囲に見えるか。見えないフィールドは無いものとして扱う。
	Visible(min string) bool
}

func invalid(format string, args ...any) *Error {
	return Errorf(CodeInvalidArgument, format, args...)
}

func decodeInput(v reflect.Value, p *structPlan, s manifest.Schema, src InputSource) error {
	body, err := src.Body()
	if err != nil {
		return invalid("body: %v", err)
	}
	known := map[string]bool{}
	for i, f := range p.fields {
		loc := s.Fields[i].In
		fv := v.FieldByIndex(f.index)
		visible := src.Visible(f.min)
		if loc == "body" && visible {
			known[f.name] = true
		}
		var present bool
		switch loc {
		case "path":
			raw := src.PathValue(f.name)
			present = raw != ""
			if present && visible {
				if err := setScalar(fv, raw); err != nil {
					return invalid("%s: %v", f.name, err)
				}
			}
		case "query":
			vals, ok := src.QueryValues(f.name)
			present = ok && len(vals) > 0
			if present && !visible {
				return invalid("unknown query parameter %q", f.name)
			}
			if present {
				if len(vals) > 1 {
					return invalid("%s: given more than once", f.name)
				}
				if err := setScalar(fv, vals[0]); err != nil {
					return invalid("%s: %v", f.name, err)
				}
			}
		case "body":
			raw, ok := body[f.name]
			present = ok && visible
			if present {
				if err := decodeJSON(raw, fv, src.Visible); err != nil {
					return invalid("%s: %v", f.name, err)
				}
			}
		}
		if !present && visible && !f.optional {
			return invalid("%s is required", f.name)
		}
	}
	for k := range body {
		if !known[k] {
			return invalid("unknown field %q", k)
		}
	}
	return nil
}

// decodeJSON は JSON の値を、api タグに従って v に入れる。
// visible で見えないフィールドは無いものとして扱う（送られれば unknown field）。
// null はポインターのフィールドだけ受ける（必須フィールドや配列の要素の null はゼロ値にしない）。
func decodeJSON(raw json.RawMessage, v reflect.Value, visible func(string) bool) error {
	if string(raw) == "null" {
		if v.Kind() != reflect.Pointer {
			return fmt.Errorf("null is not allowed")
		}
		v.SetZero()
		return nil
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return decodeJSON(raw, v.Elem(), visible)
	}
	t := v.Type()
	if t != timeType && t != bytesType {
		switch t.Kind() {
		case reflect.Struct:
			p, err := planFor(t)
			if err != nil {
				return err
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				return err
			}
			for _, f := range p.fields {
				if !visible(f.min) {
					continue
				}
				r, ok := obj[f.name]
				if !ok {
					if !f.optional {
						return fmt.Errorf("%s is required", f.name)
					}
					continue
				}
				if err := decodeJSON(r, v.FieldByIndex(f.index), visible); err != nil {
					return fmt.Errorf("%s: %w", f.name, err)
				}
				delete(obj, f.name)
			}
			for k := range obj {
				return fmt.Errorf("unknown field %q", k)
			}
			return nil
		case reflect.Slice:
			var arr []json.RawMessage
			if err := json.Unmarshal(raw, &arr); err != nil {
				return err
			}
			s := reflect.MakeSlice(t, len(arr), len(arr))
			for i, r := range arr {
				if err := decodeJSON(r, s.Index(i), visible); err != nil {
					return fmt.Errorf("[%d]: %w", i, err)
				}
			}
			v.Set(s)
			return nil
		}
	}
	return json.Unmarshal(raw, v.Addr().Interface())
}

func setScalar(v reflect.Value, raw string) error {
	if v.Kind() == reflect.Pointer {
		v.Set(reflect.New(v.Type().Elem()))
		v = v.Elem()
	}
	if v.Type() == timeType {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(t))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(raw, v.Type().Bits())
		if err != nil {
			return err
		}
		v.SetFloat(n)
	default:
		return fmt.Errorf("type %s cannot be given as text", v.Type())
	}
	return nil
}

func scalarString(v reflect.Value) string {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Type() == timeType {
		return v.Interface().(time.Time).Format(time.RFC3339Nano)
	}
	return fmt.Sprint(v.Interface())
}

// Encode は出力を、公開範囲に見えるフィールドだけの JSON にできる値にする（design.md 4.1 のフィールドの絞り込み）。
// タグの無いフィールドは出さない。Encoder を実装する値は、自分で絞り込む。
func Encode(value any, visible func(min string) bool) (any, error) {
	if e, ok := value.(Encoder); ok {
		return e.EncodeVisible(visible)
	}
	return encode(reflect.ValueOf(value), visible)
}

// Encoder は、Go の型を持たない出力（サイドカーが本体から受けた JSON など）が自分で絞り込むためのもの。
// 返す値は Encode と同じ形にする：object は map[string]any、array は []any、date-time は time.Time、byte は []byte。
type Encoder interface {
	EncodeVisible(visible func(min string) bool) (any, error)
}

func encode(v reflect.Value, visible func(string) bool) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	t := v.Type()
	if t == timeType || t == bytesType {
		return v.Interface(), nil
	}
	switch t.Kind() {
	case reflect.Struct:
		p, err := planFor(t)
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, len(p.fields))
		for _, f := range p.fields {
			if !visible(f.min) {
				continue
			}
			fv, err := encode(v.FieldByIndex(f.index), visible)
			if err != nil {
				return nil, err
			}
			out[f.name] = fv
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := range out {
			ev, err := encode(v.Index(i), visible)
			if err != nil {
				return nil, err
			}
			out[i] = ev
		}
		return out, nil
	}
	return v.Interface(), nil
}
