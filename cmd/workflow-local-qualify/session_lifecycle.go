package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/google/uuid"
)

// These constants are not flags or test-injected clocks. Qualification uses
// actual monotonic elapsed time; the extra second avoids a boundary-only sample.
const sessionLifetime = 300 * time.Second
const sessionMidpoint = 150 * time.Second
const sessionExpiryObservation = sessionLifetime + time.Second

// Local evidence metadata is distinct from the generated business DTOs. Never
// add a complete EditorSessionSecret, private file, header, E or E hash here.
type sessionEvidence struct {
	SchemaVersion           int                    `json:"schema_version"`
	Qualification           string                 `json:"qualification"`
	Complete                bool                   `json:"complete"`
	StartedAtMS             int64                  `json:"started_at_ms"`
	FinishedAtMS            int64                  `json:"finished_at_ms"`
	RunEpoch                string                 `json:"run_epoch"`
	WorkflowID              string                 `json:"workflow_id,omitempty"`
	Revision                string                 `json:"revision,omitempty"`
	CanvasSHA256            string                 `json:"canvas_sha256,omitempty"`
	CreateOperationID       string                 `json:"create_operation_id"`
	CreateReceiptBefore     *wire.OperationReceipt `json:"create_receipt_before,omitempty"`
	CreateReceiptAfter      *wire.OperationReceipt `json:"create_receipt_after,omitempty"`
	FirstOpenStartedAtMS    int64                  `json:"first_open_started_at_ms,omitempty"`
	FirstOpenReceivedAtMS   int64                  `json:"first_open_received_at_ms,omitempty"`
	FirstServerExpiresAtMS  int64                  `json:"first_server_expires_at_ms,omitempty"`
	SecondOpenReceivedAtMS  int64                  `json:"second_open_received_at_ms,omitempty"`
	SecondServerExpiresAtMS int64                  `json:"second_server_expires_at_ms,omitempty"`
	SecondCloseElapsedMS    int64                  `json:"second_close_elapsed_ms"`
	NaturalElapsedMS        int64                  `json:"natural_elapsed_ms"`
	MidpointReadElapsedMS   int64                  `json:"midpoint_read_elapsed_ms"`
	NewSessionDistinct      bool                   `json:"new_session_distinct"`
	ReopenedSameResource    bool                   `json:"reopened_same_resource"`
	ClosedBeforeExpiry      bool                   `json:"closed_before_expiry"`
	WorkflowUnchanged       bool                   `json:"workflow_unchanged"`
	CreateReceiptUnchanged  bool                   `json:"create_receipt_unchanged"`
	NoExecutions            bool                   `json:"no_executions"`
	KnownSessionsClosed     bool                   `json:"known_sessions_closed"`
	Stage                   string                 `json:"stage"`
	Observations            []sessionObservation   `json:"observations"`
}

type sessionObservation struct {
	Stage      string         `json:"stage"`
	ElapsedMS  int64          `json:"elapsed_ms"`
	HTTPStatus int            `json:"http_status"`
	ErrorCode  wire.ErrorCode `json:"error_code,omitempty"`
}

func (c *client) runSessionLifecycle(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("evidence path must be absolute and clean")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("a new session qualification evidence file could not be created")
	}
	e := &sessionEvidence{SchemaVersion: 1, Qualification: "api_editor_session_lifecycle", RunEpoch: c.credential.RunEpoch, StartedAtMS: time.Now().UnixMilli(), CreateOperationID: uuid.NewString(), Observations: []sessionObservation{}}
	qualificationErr := c.qualifySessionLifecycle(ctx, e)
	e.FinishedAtMS = time.Now().UnixMilli()
	e.Complete = qualificationErr == nil
	data, encodeErr := json.MarshalIndent(e, "", "  ")
	if encodeErr == nil {
		_, encodeErr = f.Write(append(data, '\n'))
	}
	if encodeErr == nil {
		encodeErr = f.Sync()
	}
	closeErr := f.Close()
	if encodeErr != nil || closeErr != nil {
		return errors.New("session qualification evidence could not be persisted; inspect its create operation before another write")
	}
	if qualificationErr != nil {
		return qualificationErr
	}
	fmt.Printf("API session natural lifecycle: PASS; elapsed_ms=%d; workflow_id=%s; no workflow execution or App qualification\n", e.NaturalElapsedMS, e.WorkflowID)
	return nil
}

func (c *client) qualifySessionLifecycle(ctx context.Context, e *sessionEvidence) (result error) {
	started := time.Now()
	observe := func(requestContext context.Context, stage, method, path, inSchema, outSchema string, in, out any, expected int, editor bool) error {
		e.Stage = stage
		err := c.do(requestContext, method, path, inSchema, outSchema, in, out, expected, editor)
		item := sessionObservation{Stage: stage, ElapsedMS: time.Since(started).Milliseconds(), HTTPStatus: c.lastStatus}
		if failure, ok := out.(*wire.ErrorResponse); err == nil && ok {
			item.ErrorCode = failure.Code
		}
		e.Observations = append(e.Observations, item)
		return err
	}
	closeSession := func(closeContext context.Context, stage string) error {
		var closed wire.CloseResult
		err := observe(closeContext, stage, "DELETE", "/v1/workflow-local/editor-sessions/"+url.PathEscape(c.session.SessionId), "", "CloseResult", nil, &closed, 200, false)
		if err != nil {
			return err
		}
		if !closed.Closed {
			return errors.New("session qualification close did not confirm revocation")
		}
		return nil
	}
	defer func() {
		if c.session.SessionId != "" {
			// Normal cancellation also attempts ordinary revocation, without forcing
			// any service or retaining E in an artifact. Do not overwrite the failed stage.
			stage := e.Stage
			closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cleanupErr := closeSession(closeContext, "cleanup_close")
			cancel()
			e.KnownSessionsClosed = cleanupErr == nil
			if result != nil {
				e.Stage = stage
			} else if cleanupErr != nil {
				result = cleanupErr
			}
		}
		c.session = wire.EditorSessionSecret{}
	}()
	create := wire.CreateRequest{Name: "FEAT-153 API会话自然生命周期 " + uuid.NewString()[:8], OperationId: e.CreateOperationID}
	var original wire.Workflow
	if err := observe(ctx, "create", "POST", "/v1/workflows", "CreateRequest", "Workflow", create, &original, 201, false); err != nil {
		return err
	}
	e.WorkflowID, e.Revision = original.WorkflowId, original.Revision
	hash := sha256.Sum256([]byte(original.Canvas))
	e.CanvasSHA256 = hex.EncodeToString(hash[:])
	if original.PublishedVersion != nil {
		return errors.New("new session qualification resource already has a published version")
	}
	base := "/v1/workflows/" + url.PathEscape(original.WorkflowId)
	operationPath := "/v1/workflow-local/operations/" + e.CreateOperationID
	var before wire.OperationReceipt
	if err := observe(ctx, "create_receipt", "GET", operationPath, "", "OperationReceipt", nil, &before, 200, false); err != nil {
		return err
	}
	if before.OperationId != e.CreateOperationID || before.Kind != wire.OperationKindCreate || before.Phase != wire.Completed || before.WorkflowId == nil || *before.WorkflowId != original.WorkflowId || before.Revision == nil || *before.Revision != original.Revision || before.Version != nil || before.RunId != nil {
		return errors.New("session qualification create receipt differs from the new resource")
	}
	e.CreateReceiptBefore = &before
	open := func(stage string) error {
		if err := observe(ctx, stage, "POST", "/v1/workflow-local/editor-sessions", "EditorOpenRequest", "EditorSessionSecret", wire.EditorOpenRequest{WorkflowId: original.WorkflowId}, &c.session, 201, false); err != nil {
			return err
		}
		if c.session.WorkflowId != original.WorkflowId || c.session.RunEpoch != c.credential.RunEpoch || c.session.ExpiresAtMs <= time.Now().UnixMilli() {
			return errors.New("session qualification binding or expiry differs")
		}
		return nil
	}
	bootstrap := func(stage string) error {
		var value wire.Bootstrap
		if err := observe(ctx, stage, "GET", base+"/bootstrap", "", "Bootstrap", nil, &value, 200, true); err != nil {
			return err
		}
		if !reflect.DeepEqual(value.Workflow, original) || value.Principal.OwnerId != domain.OwnerID || value.Principal.TenantId != domain.TenantID {
			return errors.New("session qualification bootstrap changed resource or scope")
		}
		return nil
	}
	expired := func(stage string) error {
		var failure wire.ErrorResponse
		if err := observe(ctx, stage, "GET", base+"/bootstrap", "", "ErrorResponse", nil, &failure, 401, true); err != nil {
			return err
		}
		if failure.Code != wire.SessionExpired || failure.OperationId != nil {
			return errors.New("session qualification expected the source session_expired result")
		}
		return nil
	}
	e.FirstOpenStartedAtMS = time.Now().UnixMilli()
	if err := open("open_first"); err != nil {
		return err
	}
	received := time.Now()
	e.FirstOpenReceivedAtMS, e.FirstServerExpiresAtMS = received.UnixMilli(), c.session.ExpiresAtMs
	firstSecretDigest, firstSessionDigest := sha256.Sum256([]byte(c.session.Secret)), sha256.Sum256([]byte(c.session.SessionId))
	if err := bootstrap("bootstrap_initial"); err != nil {
		return err
	}
	e.Stage = "natural_midpoint_wait"
	if err := waitSessionDeadline(ctx, received, received.Add(sessionMidpoint)); err != nil {
		return err
	}
	if err := bootstrap("bootstrap_midpoint_no_renewal"); err != nil {
		return err
	}
	e.MidpointReadElapsedMS = time.Since(received).Milliseconds()
	e.Stage = "natural_expiry_wait"
	if err := waitSessionDeadline(ctx, received, received.Add(sessionExpiryObservation)); err != nil {
		return err
	}
	e.NaturalElapsedMS = time.Since(received).Milliseconds()
	if e.NaturalElapsedMS < sessionLifetime.Milliseconds() {
		return errors.New("session qualification did not observe the full natural lifetime")
	}
	if err := expired("natural_expiry_rejected"); err != nil {
		return err
	}
	for _, stage := range []string{"expired_close", "expired_close_idempotent"} {
		if err := closeSession(ctx, stage); err != nil {
			return err
		}
	}
	c.session = wire.EditorSessionSecret{}
	e.KnownSessionsClosed = true
	if err := open("open_explicit_reconnect"); err != nil {
		return err
	}
	secondReceived := time.Now()
	e.SecondOpenReceivedAtMS, e.SecondServerExpiresAtMS = secondReceived.UnixMilli(), c.session.ExpiresAtMs
	e.KnownSessionsClosed = false
	e.NewSessionDistinct = firstSecretDigest != sha256.Sum256([]byte(c.session.Secret)) && firstSessionDigest != sha256.Sum256([]byte(c.session.SessionId))
	if !e.NewSessionDistinct {
		return errors.New("explicit reconnect reused the original session")
	}
	if err := bootstrap("bootstrap_reconnected"); err != nil {
		return err
	}
	e.ReopenedSameResource = true
	if err := closeSession(ctx, "close_before_expiry"); err != nil {
		return err
	}
	e.SecondCloseElapsedMS = time.Since(secondReceived).Milliseconds()
	e.ClosedBeforeExpiry = e.SecondCloseElapsedMS < sessionLifetime.Milliseconds() && time.Now().UnixMilli() < c.session.ExpiresAtMs
	if !e.ClosedBeforeExpiry {
		return errors.New("second session was not closed within its natural lifetime")
	}
	if err := expired("closed_session_rejected"); err != nil {
		return err
	}
	c.session = wire.EditorSessionSecret{}
	e.KnownSessionsClosed = true
	var after wire.Workflow
	if err := observe(ctx, "machine_read_after_close", "GET", base, "", "Workflow", nil, &after, 200, false); err != nil {
		return err
	}
	e.WorkflowUnchanged = reflect.DeepEqual(original, after)
	var receipt wire.OperationReceipt
	if err := observe(ctx, "machine_create_receipt_after_close", "GET", operationPath, "", "OperationReceipt", nil, &receipt, 200, false); err != nil {
		return err
	}
	e.CreateReceiptAfter = &receipt
	e.CreateReceiptUnchanged = reflect.DeepEqual(before, receipt)
	var history wire.RunList
	if err := observe(ctx, "machine_empty_history", "GET", base+"/runs", "", "RunList", nil, &history, 200, false); err != nil {
		return err
	}
	e.NoExecutions = len(history.Items) == 0 && history.NextCursor == nil
	if !e.WorkflowUnchanged || !e.CreateReceiptUnchanged || !e.NoExecutions {
		return errors.New("session-only qualification changed workflow, receipt or execution history")
	}
	e.Stage = "completed"
	return nil
}

func waitSessionDeadline(ctx context.Context, started, deadline time.Time) error {
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		if remaining > 30*time.Second {
			remaining = 30 * time.Second
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("session qualification interrupted; normal session cleanup will run")
		case <-timer.C:
			fmt.Printf("API session natural wait: elapsed_seconds=%d\n", int64(time.Since(started)/time.Second))
		}
	}
}
