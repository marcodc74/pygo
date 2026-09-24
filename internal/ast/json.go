package ast

import (
	"fmt"
	"reflect"
)

// ToJSON converts a node into plain maps/slices suitable for encoding/json.
// Every node object carries "node": "<Type>" and "pos": "line:col".
func ToJSON(n any) any {
	return toJSON(reflect.ValueOf(n))
}

var posType = reflect.TypeOf(Pos{})

func toJSON(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return toJSON(v.Elem())
	case reflect.Struct:
		if v.Type() == posType {
			p := v.Interface().(Pos)
			return fmt.Sprintf("%d:%d", p.Line, p.Col)
		}
		out := map[string]any{"node": v.Type().Name()}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || f.Name == "Start" || f.Name == "End" {
				continue
			}
			fv := v.Field(i)
			if fv.IsZero() && f.Name != "Value" {
				continue
			}
			key := jsonName(f.Name)
			out[key] = toJSON(fv)
		}
		return out
	case reflect.Slice:
		arr := make([]any, v.Len())
		for i := range arr {
			arr[i] = toJSON(v.Index(i))
		}
		return arr
	default:
		return v.Interface()
	}
}

func jsonName(s string) string {
	b := []rune{}
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b = append(b, '_')
			}
			r += 'a' - 'A'
		}
		b = append(b, r)
	}
	return string(b)
}
