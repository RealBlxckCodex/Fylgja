package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateArgs prüft Argumente gegen ein JSON-Schema (Teilmenge: type, required,
// properties, enum, items, additionalProperties=false). Rekursiv.
func ValidateArgs(schema json.RawMessage, args json.RawMessage) (map[string]any, error) {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	var v any
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if err := json.Unmarshal(args, &v); err != nil {
		return nil, fmt.Errorf("argumente sind kein gültiges json: %w", err)
	}
	if err := validate(s, v, "$"); err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func validate(s map[string]any, v any, path string) error {
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s: wert %v nicht erlaubt", path, v)
		}
	}
	typ, _ := s["type"].(string)
	switch typ {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: objekt erwartet", path)
		}
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				k := fmt.Sprint(r)
				if _, ok := m[k]; !ok {
					return fmt.Errorf("%s.%s fehlt", path, k)
				}
			}
		}
		if ap, ok := s["additionalProperties"].(bool); ok && !ap {
			for k := range m {
				if _, ok := props[k]; !ok {
					return fmt.Errorf("%s.%s ist nicht erlaubt", path, k)
				}
			}
		}
		for k, ps := range props {
			if val, ok := m[k]; ok {
				if sub, ok := ps.(map[string]any); ok {
					if err := validate(sub, val, path+"."+k); err != nil {
						return err
					}
				}
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: array erwartet", path)
		}
		if it, ok := s["items"].(map[string]any); ok {
			for i, x := range a {
				if err := validate(it, x, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s: string erwartet", path)
		}
		if ml, ok := s["maxLength"].(float64); ok && float64(len(v.(string))) > ml {
			return fmt.Errorf("%s: zu lang", path)
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("%s: ganzzahl erwartet", path)
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("%s: zahl erwartet", path)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: bool erwartet", path)
		}
	case "":
	default:
		if strings.Contains(typ, "null") && v == nil {
			return nil
		}
	}
	return nil
}
