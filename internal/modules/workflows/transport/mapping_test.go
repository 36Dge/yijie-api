package transport

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
)

func TestEmptyNodeAndRunOutputPreserved(t *testing.T) {
	empty := ""
	input := wire.Run{RunId: "1", WorkflowId: "2", OperationId: "15300000-0000-4000-8000-000000000002", Mode: wire.Debug, State: "succeeded", Terminal: true, StartedAtMs: 1, Output: &empty, Nodes: &[]wire.NodeResult{{NodeId: "end", State: "succeeded", Output: &empty}}}
	data, _ := json.Marshal(input)
	r, err := DecodeResponse("read_run", data)
	if err != nil {
		t.Fatal(err)
	}
	out := Run(*r.Run)
	if out.Output == nil || *out.Output != "" || out.Nodes == nil || (*out.Nodes)[0].Output == nil || *(*out.Nodes)[0].Output != "" {
		t.Fatal("empty output was confused with unavailable")
	}
}
func TestNormalMultibyteInputUsesUTF8Limit(t *testing.T) {
	input := wire.RunRequest{OperationId: "15300000-0000-4000-8000-000000000002", Version: "v0.0.1", Input: wire.TextInput{Input: strings.Repeat("文", 1365)}}
	data, _ := json.Marshal(input)
	if _, err := ParseRequest("run", data); err != nil {
		t.Fatal("4095 UTF-8 bytes should fit")
	}
	input.Input.Input += "文"
	data, _ = json.Marshal(input)
	_, err := ParseRequest("run", data)
	var f *domain.Failure
	if !errors.As(err, &f) || f.Code != "input_too_large" {
		t.Fatal("4098 UTF-8 bytes must return size error")
	}
}

func TestCreateDescriptionRoundTripAndLegacyIntentBytes(t *testing.T) {
	legacy := []byte(`{"name":"中文流程","operation_id":"15300000-0000-4000-8000-000000000001"}`)
	r, err := ParseRequest("create", legacy)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"Action":"create","WorkflowID":"","RunID":"","OperationID":"15300000-0000-4000-8000-000000000001","Name":"中文流程","Revision":"","Canvas":"","Input":"","Version":"","TestRunID":"","Cursor":"","Limit":0}`
	if string(encoded) != want {
		t.Fatalf("legacy intent bytes changed: %s", encoded)
	}
	description := "本地文本用途"
	r.Description = &description
	_, body := RequestBody(r)
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := ParseRequest("create", data)
	if err != nil || restored.Description == nil || *restored.Description != description {
		t.Fatal("description mapping lost")
	}
}
