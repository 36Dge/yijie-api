package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/transport"
)

func TestNormalQualificationModesRemainExplicit(t *testing.T) {
	for _, mode := range []struct {
		name, create, verify string
		session              bool
	}{
		{"ordinary workflow", "/normal/workflow.json", "", false},
		{"normal persistence reads", "", "/normal/workflow.json", false},
		{"natural session lifecycle", "/normal/session.json", "", true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			if !validQualificationMode(mode.create, mode.verify, mode.session) {
				t.Fatal("a normal explicit qualification mode is unavailable")
			}
		})
	}
}

func TestSessionScheduleKeepsTheSourceNaturalLifetime(t *testing.T) {
	if sessionLifetime != time.Duration(transport.Limits().EditorTtlSeconds)*time.Second || sessionMidpoint <= 0 || sessionMidpoint >= sessionLifetime || sessionExpiryObservation < sessionLifetime {
		t.Fatal("qualification schedule must preserve the source's complete natural lifetime")
	}
	// An already completed ordinary wait returns immediately. This does not run
	// qualifySessionLifecycle, shorten its TTL or advance any clock/deadline.
	now := time.Now()
	if err := waitSessionDeadline(context.Background(), now, now); err != nil {
		t.Fatal("already completed natural wait did not return normally")
	}
}

type normalRoundTrip func(*http.Request) (*http.Response, error)

func (run normalRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return run(request)
}

func TestSessionObservationReadsSourceStatusWithoutAListener(t *testing.T) {
	// Ordinary protocol response projections, not a live expiry/failure test.
	// The transport is in-process and opens no port or network connection.
	for _, item := range []struct {
		status int
		schema string
		value  any
		out    any
	}{
		{200, "CloseResult", wire.CloseResult{Closed: true}, &wire.CloseResult{}},
		{401, "ErrorResponse", wire.ErrorResponse{Code: wire.SessionExpired, Message: "Editor session expired"}, &wire.ErrorResponse{}},
	} {
		payload, err := json.Marshal(item.value)
		if err != nil || transport.Validate(item.schema, payload) != nil {
			t.Fatal("ordinary response does not conform to the consumed source")
		}
		c := &client{http: &http.Client{Transport: normalRoundTrip(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme != "http" || request.URL.Host != "127.0.0.1:18888" {
				t.Error("qualifier destination is no longer fixed")
			}
			return &http.Response{StatusCode: item.status, Header: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
		})}}
		if err := c.do(context.Background(), "GET", "/v1/workflows/normal/bootstrap", "", item.schema, nil, item.out, item.status, false); err != nil {
			t.Fatal(err)
		}
		if c.lastStatus != item.status {
			t.Fatal("evidence observation lost the actual response status")
		}
	}
}

func TestSessionEvidenceContainsSourceReceiptsButNoSessionMaterial(t *testing.T) {
	id := "15300000-0000-4000-8000-000000000001"
	workflowID := "7684523784379826176"
	receipt := wire.OperationReceipt{OperationId: id, Kind: wire.OperationKindCreate, Phase: wire.Completed, WorkflowId: &workflowID}
	e := sessionEvidence{SchemaVersion: 1, Qualification: "api_editor_session_lifecycle", CreateOperationID: id, CreateReceiptBefore: &receipt, CreateReceiptAfter: &receipt, NaturalElapsedMS: sessionExpiryObservation.Milliseconds(), Observations: []sessionObservation{{Stage: "natural_expiry_rejected", HTTPStatus: 401, ErrorCode: wire.SessionExpired}}}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal("normal session evidence cannot be encoded")
	}
	var decoded sessionEvidence
	if json.Unmarshal(data, &decoded) != nil || !reflect.DeepEqual(e, decoded) {
		t.Fatal("normal nonsecret evidence changed during encoding")
	}
	for _, value := range []*wire.OperationReceipt{decoded.CreateReceiptBefore, decoded.CreateReceiptAfter} {
		raw, err := json.Marshal(value)
		if err != nil || transport.Validate("OperationReceipt", raw) != nil {
			t.Fatal("evidence receipt is not source-conformant")
		}
	}
	for _, field := range []string{`"secret"`, `"session_id"`, `"token"`, `"credential"`, `"headers"`} {
		if strings.Contains(string(data), field) {
			t.Fatal("session evidence contains private transport material")
		}
	}
}
