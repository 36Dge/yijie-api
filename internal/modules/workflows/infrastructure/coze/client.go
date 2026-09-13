package coze

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/36Dge/yijie-api/internal/modules/workflows/transport"
)

type Client struct {
	base, token, epoch string
	http               *http.Client
}

func New(base, token, epoch string) *Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &Client{base: base, token: token, epoch: epoch, http: &http.Client{Transport: t, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Execute(ctx context.Context, r domain.Request) (domain.Response, error) {
	method, path, status := route(r)
	if path == "" {
		return domain.Response{}, domain.Fail("invalid_request")
	}
	if r.WorkflowID != "" {
		data, _ := json.Marshal(r.WorkflowID)
		if transport.Validate("Identifier", data) != nil {
			return domain.Response{}, domain.Fail("invalid_request")
		}
	}
	if r.RunID != "" {
		data, _ := json.Marshal(r.RunID)
		if transport.Validate("Identifier", data) != nil {
			return domain.Response{}, domain.Fail("invalid_request")
		}
	}
	if r.Action == "operation" {
		data, _ := json.Marshal(r.OperationID)
		if transport.Validate("OperationId", data) != nil {
			return domain.Response{}, domain.Fail("invalid_request")
		}
	}
	if r.Action == "list" || r.Action == "history" {
		q := url.Values{}
		q.Set("limit", strconv.FormatInt(r.Limit, 10))
		if r.Cursor != "" {
			q.Set("cursor", r.Cursor)
		}
		path += "?" + q.Encode()
	}
	var body []byte
	if name, v := transport.RequestBody(r); v != nil {
		var err error
		body, err = json.Marshal(v)
		if err != nil || len(body) > domain.MessageBytes || transport.Validate(name, body) != nil {
			return domain.Response{}, domain.Fail("invalid_request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return domain.Response{}, domain.Fail("service_unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Yijie-Run-Epoch", c.epoch)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	// Build a new header set; native E, cookies and arbitrary client headers never cross this boundary.
	resp, err := c.http.Do(req)
	if err != nil {
		return domain.Response{}, domain.Fail("service_unavailable")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, domain.MessageBytes+1))
	if err != nil || len(data) > domain.MessageBytes {
		return domain.Response{}, domain.Fail("protocol_mismatch")
	}
	if resp.StatusCode != status {
		if transport.Validate("ErrorResponse", data) != nil {
			return domain.Response{}, domain.Fail("protocol_mismatch")
		}
		var e wire.ErrorResponse
		if json.Unmarshal(data, &e) != nil {
			return domain.Response{}, domain.Fail("protocol_mismatch")
		}
		failure := &domain.Failure{Code: string(e.Code), OperationID: r.OperationID}
		if transport.ErrorStatus(failure) != resp.StatusCode {
			return domain.Response{}, domain.Fail("protocol_mismatch")
		}
		return domain.Response{}, failure
	}
	return transport.DecodeResponse(r.Action, data)
}

func route(r domain.Request) (string, string, int) {
	base := "/v1/workflows/" + url.PathEscape(r.WorkflowID)
	switch r.Action {
	case "status":
		return "GET", "/v1/workflow-local/status", 200
	case "principal":
		return "POST", "/v1/workflow-local/principal", 200
	case "list":
		return "GET", "/v1/workflows", 200
	case "create":
		return "POST", "/v1/workflows", 201
	case "read":
		return "GET", base, 200
	case "save":
		return "PUT", base, 200
	case "bootstrap":
		return "GET", base + "/bootstrap", 200
	case "test":
		return "POST", base + "/test-runs", 202
	case "publish":
		return "POST", base + "/versions", 200
	case "run":
		return "POST", base + "/runs", 202
	case "history":
		return "GET", base + "/runs", 200
	case "read_run":
		return "GET", base + "/runs/" + url.PathEscape(r.RunID), 200
	case "operation":
		return "GET", "/v1/workflow-local/operations/" + url.PathEscape(r.OperationID), 200
	default:
		return "", "", 0
	}
}
