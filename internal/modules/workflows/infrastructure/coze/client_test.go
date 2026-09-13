package coze

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/36Dge/yijie-api/internal/modules/workflows/transport"
)

func TestClientAuthenticatedSourceConformantUnknownRun(t *testing.T) {
	epoch := "15300000-0000-4000-8000-000000000001"
	operation := "15300000-0000-4000-8000-000000000002"
	token := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/workflows/123/runs" {
			t.Error("route differs")
		}
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Yijie-Run-Epoch") != epoch {
			t.Error("service identity differs")
		}
		if r.Header.Get("X-Yijie-Editor-Session") != "" || r.Header.Get("Cookie") != "" {
			t.Error("native session leaked into engine")
		}
		var body wire.RunRequest
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.OperationId != operation || body.Version != "v0.0.1" {
			t.Error("request conformance")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(wire.Run{RunId: "345", WorkflowId: "123", OperationId: operation, Mode: wire.Release, Version: &body.Version, State: "unknown", Terminal: false, StartedAtMs: 1})
	}))
	defer server.Close()
	client := New(server.URL, token, epoch)
	defer client.Close()
	result, err := client.Execute(context.Background(), domain.Request{Action: "run", WorkflowID: "123", OperationID: operation, Version: "v0.0.1", Input: "ordinary text"})
	if err != nil || result.Run == nil || result.Run.Terminal || result.Run.State != "unknown" {
		t.Fatalf("engine fact changed: %v", err)
	}
}
func TestClientNormalListUsesSummaryContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "20" {
			t.Error("page bound differs")
		}
		_ = json.NewEncoder(w).Encode(wire.WorkflowList{Items: []wire.WorkflowSummary{{WorkflowId: "123", Name: "Synthetic", Revision: "r1", UpdatedAtMs: 1}}})
	}))
	defer server.Close()
	client := New(server.URL, strings.Repeat("b", 64), "15300000-0000-4000-8000-000000000001")
	defer client.Close()
	result, err := client.Execute(context.Background(), domain.Request{Action: "list", Limit: 20})
	if err != nil || len(result.Workflows) != 1 {
		t.Fatalf("summary: %v", err)
	}
	name, out, err := transport.EncodeResponse("list", result)
	data, _ := json.Marshal(out)
	if err != nil || name != "WorkflowList" || transport.Validate(name, data) != nil || strings.Contains(string(data), "canvas") {
		t.Fatal("list leaked a full canvas")
	}
}
