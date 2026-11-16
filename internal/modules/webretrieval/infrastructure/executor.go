package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/36Dge/yijie-api/internal/platform/webwire"
)

type Executor struct {
	Client *http.Client
	Origin string
}

func (e *Executor) Execute(ctx context.Context, o d.Operation, q d.Request) ([]byte, error) {
	raw, err := json.Marshal(map[string]any{"operation_id": q.ID.String(), "tool": q.Tool, "arguments": json.RawMessage(q.Arguments)})
	if err != nil {
		return nil, d.Invalid
	}
	var request wire.OperationRequest
	if err = request.UnmarshalJSON(raw); err != nil {
		return nil, d.Invalid
	}
	payload, err := json.Marshal(wire.InternalExecute{BindingId: o.Binding.ID, Request: request, ParameterDigest: o.Digest, DeadlineUnixMs: int(o.Deadline.UnixMilli())})
	if err != nil {
		return nil, d.Invalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Origin+"/internal/v1/web-retrieval/execute", bytes.NewReader(payload))
	if err != nil {
		return nil, d.Unavailable
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := e.Client.Do(req)
	if err != nil {
		return nil, d.Unavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, d.Unavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, wire.MaxResultBytes+4097))
	if err != nil || len(body) > wire.MaxResultBytes+4096 {
		return nil, d.Unavailable
	}
	if webwire.Validate("ExecutionResult", body) != nil {
		return nil, d.Unavailable
	}
	var result wire.ExecutionResult
	if json.Unmarshal(body, &result) != nil || result.Operation.OperationId != o.ID.String() || result.Result == nil {
		return nil, d.Unavailable
	}
	return result.Result.MarshalJSON()
}
