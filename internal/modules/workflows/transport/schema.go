// Package transport maps source-generated wire values to private domain values.
package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	contracts "github.com/36Dge/yijie-api/contracts"
)

var source map[string]any
var ErrSize = errors.New("workflow value exceeds source size limit")

func init() {
	if err := json.Unmarshal(contracts.WorkflowSchema, &source); err != nil {
		panic("invalid embedded workflow contract")
	}
}

// Validate uses the consumed source schema for every HTTP producer and consumer value.
func Validate(name string, data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8")
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return fmt.Errorf("invalid JSON")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	defs, _ := source["$defs"].(map[string]any)
	schema, ok := defs[name].(map[string]any)
	if !ok {
		return fmt.Errorf("unknown schema %s", name)
	}
	return visit(schema, value)
}
func visit(schema map[string]any, value any) error {
	if ref, ok := schema["$ref"].(string); ok {
		part := strings.TrimPrefix(ref, "#/$defs/")
		defs, _ := source["$defs"].(map[string]any)
		s, ok := defs[part].(map[string]any)
		if !ok {
			return fmt.Errorf("invalid schema reference")
		}
		return visit(s, value)
	}
	if values, ok := schema["enum"].([]any); ok {
		found := false
		actual, _ := json.Marshal(value)
		for _, v := range values {
			expected, _ := json.Marshal(v)
			if bytes.Equal(actual, expected) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("unknown enum")
		}
	}
	switch schema["type"] {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object")
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, key := range array(schema["required"]) {
			if _, ok := obj[key.(string)]; !ok {
				return fmt.Errorf("missing field")
			}
		}
		for key, v := range obj {
			s, exists := properties[key]
			if !exists {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("unknown field")
				}
				continue
			}
			if err := visit(s.(map[string]any), v); err != nil {
				return err
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected array")
		}
		if max, ok := schema["maxItems"].(float64); ok && len(items) > int(max) {
			return fmt.Errorf("too many items")
		}
		for _, v := range items {
			if err := visit(schema["items"].(map[string]any), v); err != nil {
				return err
			}
		}
	case "string":
		v, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		n := utf8.RuneCountInString(v)
		if min, ok := schema["minLength"].(float64); ok && n < int(min) {
			return fmt.Errorf("short string")
		}
		if max, ok := schema["maxLength"].(float64); ok && n > int(max) {
			return ErrSize
		}
		if max, ok := schema["x-utf8-max-bytes"].(float64); ok && len(v) > int(max) {
			return ErrSize
		}
		if pattern, ok := schema["pattern"].(string); ok {
			matched, err := regexp.MatchString(pattern, v)
			if err != nil || !matched {
				return fmt.Errorf("invalid string")
			}
		}
	case "integer":
		n, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("expected integer")
		}
		i, err := n.Int64()
		if err != nil {
			return fmt.Errorf("expected integer")
		}
		if min, ok := schema["minimum"].(float64); ok && i < int64(min) {
			return fmt.Errorf("below minimum")
		}
		if max, ok := schema["maximum"].(float64); ok && i > int64(max) {
			return fmt.Errorf("above maximum")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean")
		}
	default:
		return fmt.Errorf("unsupported source type")
	}
	return nil
}
func array(v any) []any { a, _ := v.([]any); return a }
