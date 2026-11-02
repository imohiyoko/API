package define

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/imohiyoko/API/manifest"
)

// フィールドは `api` タグで API に出す。タグの無いフィールドは出さない（既定は非公開。design.md 4.2）。
//
//	`api:"name"`                  名前
//	`api:"id,path"`               入力の場所（path・query・body。既定は Query なら query、Command なら body）。
//	                              入力の一番上のフィールドでだけ効き、ほかでは無視する
//	`api:"email,min=partner"`     見せる最も狭い公開範囲
//	`api:"note,optional"`         省略できる（ポインターも省略できる）
type fieldPlan struct {
	name     string
	index    []int
	in       string
	min      string
	optional bool
	typ      reflect.Type
}

type structPlan struct {
	fields []fieldPlan
}

var (
	timeType  = reflect.TypeFor[time.Time]()
	bytesType = reflect.TypeFor[[]byte]()
	plans     sync.Map // reflect.Type → *structPlan
)

// planFor は struct のフィールドの計画を返す。struct でなければ nil。
func planFor(t reflect.Type) (*structPlan, error) {
	if t.Kind() != reflect.Struct || t == timeType {
		return nil, nil
	}
	if p, ok := plans.Load(t); ok {
		return p.(*structPlan), nil
	}
	p := &structPlan{}
	seen := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag, ok := sf.Tag.Lookup("api")
		if !ok || tag == "-" {
			continue
		}
		if !sf.IsExported() {
			return nil, fmt.Errorf("%s.%s: unexported field has an api tag", t, sf.Name)
		}
		parts := strings.Split(tag, ",")
		f := fieldPlan{name: parts[0], index: sf.Index, typ: sf.Type, optional: sf.Type.Kind() == reflect.Pointer}
		if f.name == "" {
			return nil, fmt.Errorf("%s.%s: api tag needs a name", t, sf.Name)
		}
		for _, opt := range parts[1:] {
			switch {
			case opt == "path" || opt == "query" || opt == "body":
				f.in = opt
			case opt == "optional":
				f.optional = true
			case strings.HasPrefix(opt, "min="):
				f.min = strings.TrimPrefix(opt, "min=")
			default:
				return nil, fmt.Errorf("%s.%s: unknown api tag option %q", t, sf.Name, opt)
			}
		}
		if seen[f.name] {
			return nil, fmt.Errorf("%s: duplicate api name %q", t, f.name)
		}
		seen[f.name] = true
		p.fields = append(p.fields, f)
	}
	plans.Store(t, p)
	return p, nil
}

func inputSchema(p *structPlan, defaultIn string) (manifest.Schema, error) {
	s := manifest.Schema{Type: "object", Fields: []manifest.Field{}}
	for _, f := range p.fields {
		fs, err := schemaFor(f.typ)
		if err != nil {
			return s, fmt.Errorf("%s: %w", f.name, err)
		}
		in := f.in
		if in == "" {
			in = defaultIn
		}
		s.Fields = append(s.Fields, manifest.Field{Name: f.name, In: in, Min: f.min, Optional: f.optional, Schema: fs})
	}
	return s, nil
}

// schemaFor は Go の型をマニフェストの型にする。
func schemaFor(t reflect.Type) (manifest.Schema, error) {
	return schemaRec(t, map[reflect.Type]bool{})
}

func schemaRec(t reflect.Type, visiting map[reflect.Type]bool) (manifest.Schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == timeType:
		return manifest.Schema{Type: "string", Format: "date-time"}, nil
	case t == bytesType:
		return manifest.Schema{Type: "string", Format: "byte"}, nil
	}
	switch t.Kind() {
	case reflect.String:
		return manifest.Schema{Type: "string"}, nil
	case reflect.Bool:
		return manifest.Schema{Type: "boolean"}, nil
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint8, reflect.Uint16:
		return manifest.Schema{Type: "integer", Format: "int32"}, nil
	case reflect.Int, reflect.Int64, reflect.Uint, reflect.Uint32, reflect.Uint64:
		return manifest.Schema{Type: "integer", Format: "int64"}, nil
	case reflect.Float32:
		return manifest.Schema{Type: "number", Format: "float"}, nil
	case reflect.Float64:
		return manifest.Schema{Type: "number", Format: "double"}, nil
	case reflect.Slice, reflect.Array:
		items, err := schemaRec(t.Elem(), visiting)
		if err != nil {
			return manifest.Schema{}, err
		}
		return manifest.Schema{Type: "array", Items: &items}, nil
	case reflect.Struct:
		if visiting[t] {
			return manifest.Schema{}, fmt.Errorf("recursive type %s is not supported", t)
		}
		visiting[t] = true
		defer delete(visiting, t)
		p, err := planFor(t)
		if err != nil {
			return manifest.Schema{}, err
		}
		s := manifest.Schema{Type: "object", Fields: []manifest.Field{}}
		for _, f := range p.fields {
			fs, err := schemaRec(f.typ, visiting)
			if err != nil {
				return manifest.Schema{}, fmt.Errorf("%s: %w", f.name, err)
			}
			s.Fields = append(s.Fields, manifest.Field{Name: f.name, Min: f.min, Optional: f.optional, Schema: fs})
		}
		return s, nil
	}
	return manifest.Schema{}, fmt.Errorf("type %s is not supported", t)
}
