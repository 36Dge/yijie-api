package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalIncompleteDraftAndPrefixBound(t *testing.T) {
	start := map[string]any{"id": "100001", "type": "1"}
	end := map[string]any{"id": "900001", "type": "2"}
	canvas := map[string]any{"nodes": []any{start, end}, "edges": []any{}}
	data, _ := json.Marshal(canvas)
	if ValidateCanvasEnvelope(string(data)) != nil {
		t.Fatal("normal incomplete draft was rejected")
	}
	value := map[string]any{"content": strings.Repeat("前", 341) + "{{input}}"}
	text := map[string]any{"id": "200001", "type": "15", "data": map[string]any{"inputs": map[string]any{"concatParams": []any{map[string]any{"name": "concatResult", "input": map[string]any{"value": value}}}}}}
	canvas["nodes"] = []any{start, text, end}
	data, _ = json.Marshal(canvas)
	if ValidateCanvasEnvelope(string(data)) != nil {
		t.Fatal("1023-byte prefix should fit")
	}
	value["content"] = strings.Repeat("前", 342) + "{{input}}"
	data, _ = json.Marshal(canvas)
	if ValidateCanvasEnvelope(string(data)) == nil {
		t.Fatal("prefix must stay within source byte budget")
	}
}
