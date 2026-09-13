package domain

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ValidateCanvasEnvelope checks the limited native Coze canvas shape at the API boundary.
// This map inspection is not an editable wire DTO. Coze owns full reference/runner validation.
func ValidateCanvasEnvelope(raw string) error {
	if !utf8.ValidString(raw) || len(raw) > 262144 {
		return Fail("input_too_large")
	}
	var top map[string]any
	if json.Unmarshal([]byte(raw), &top) != nil {
		return Fail("invalid_draft")
	}
	nodes, ok := top["nodes"].([]any)
	if !ok || len(nodes) < 2 || len(nodes) > 3 {
		return Fail("invalid_draft")
	}
	edges, ok := top["edges"].([]any)
	if !ok || len(edges) > 2 {
		return Fail("invalid_draft")
	}
	ids := map[string]bool{}
	kinds := map[string]bool{}
	for _, entry := range nodes {
		n, ok := entry.(map[string]any)
		if !ok {
			return Fail("invalid_draft")
		}
		id, ok := n["id"].(string)
		if !ok || id == "" || ids[id] {
			return Fail("invalid_draft")
		}
		ids[id] = true
		kind, ok := n["type"].(string)
		if !ok || (kind != "1" && kind != "15" && kind != "2") || kinds[kind] {
			return Fail("invalid_draft")
		}
		kinds[kind] = true
		for _, key := range []string{"blocks", "edges"} {
			if v, exists := n[key]; exists && v != nil {
				a, ok := v.([]any)
				if !ok || len(a) != 0 {
					return Fail("invalid_draft")
				}
			}
		}
		if kind != "15" {
			continue
		}
		data, _ := n["data"].(map[string]any)
		inputs, _ := data["inputs"].(map[string]any)
		params, _ := inputs["concatParams"].([]any)
		for _, param := range params {
			p, _ := param.(map[string]any)
			if p["name"] != "concatResult" {
				continue
			}
			input, _ := p["input"].(map[string]any)
			value, _ := input["value"].(map[string]any)
			content, ok := value["content"].(string)
			if !ok {
				return Fail("invalid_draft")
			}
			prefix := strings.TrimSuffix(content, "{{input}}")
			if len(prefix) > 1024 {
				return Fail("input_too_large")
			}
		}
	}
	if !kinds["1"] || !kinds["2"] {
		return Fail("invalid_draft")
	}
	return nil
}
