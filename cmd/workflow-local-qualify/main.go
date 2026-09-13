// workflow-local-qualify runs only the declared, ordinary local text workflow.
// It is an opt-in qualification tool, not a production or public API command.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/workflowlocal"
	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/36Dge/yijie-api/internal/modules/workflows/transport"
	"github.com/36Dge/yijie-api/internal/workflowlocal"
	"github.com/google/uuid"
)

// evidence is a local qualification artifact. Wire values use source-generated
// types; secrets, request headers and raw service errors never enter this file.
type evidence struct {
	SchemaVersion int                     `json:"schema_version"`
	Complete      bool                    `json:"complete"`
	Workflow      wire.Workflow           `json:"workflow"`
	Runs          []wire.Run              `json:"runs"`
	Operations    []wire.OperationReceipt `json:"operations"`
	AttemptedIDs  []string                `json:"attempted_operation_ids"`
}

type client struct {
	http       *http.Client
	credential workflowlocal.Credential
	session    wire.EditorSessionSecret
	lastStatus int
}

func main() {
	createPath := flag.String("evidence", "", "new absolute path for the nonsecret qualification artifact")
	verifyPath := flag.String("verify-evidence", "", "absolute artifact path to verify after a normal stack reopen; read-only")
	sessionLifecycle := flag.Bool("session-lifecycle", false, "only qualify the API editor session using natural 300-second expiry; requires --evidence, no workflow execution")
	flag.Parse()
	if flag.NArg() != 0 || !validQualificationMode(*createPath, *verifyPath, *sessionLifecycle) {
		fmt.Fprintln(os.Stderr, "choose --evidence [--session-lifecycle] or --verify-evidence")
		os.Exit(2)
	}
	if err := run(*createPath, *verifyPath, *sessionLifecycle); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validQualificationMode(createPath, verifyPath string, sessionLifecycle bool) bool {
	return (createPath == "") != (verifyPath == "") && (!sessionLifecycle || verifyPath == "")
}

func run(createPath, verifyPath string, sessionLifecycle bool) error {
	if os.Getenv("YIJIE_ENV") != "local" || os.Getenv("YIJIE_LOCAL_PROFILE") != "demo_fast" || os.Getenv("YIJIE_WORKFLOW_ENABLED") != "true" || os.Getenv("YIJIE_API_SERVICE_PROFILE") != workflowlocal.Profile {
		return errors.New("qualification requires the exact local workflow profile")
	}
	credential, err := workflowlocal.ReadCredential(os.Getenv("YIJIE_WORKFLOW_CREDENTIAL_FILE"))
	if err != nil {
		return errors.New("qualification credential is unavailable")
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	c := &client{credential: credential, http: &http.Client{Transport: t, Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	defer c.http.CloseIdleConnections()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var status wire.ServiceStatus
	if err := c.get(ctx, "/v1/workflow-local/status", "ServiceStatus", &status); err != nil {
		return err
	}
	if !status.Ready || status.RunEpoch != credential.RunEpoch {
		return errors.New("workflow service is not ready for this run epoch")
	}
	if sessionLifecycle {
		return c.runSessionLifecycle(ctx, createPath)
	}
	if verifyPath != "" {
		e, err := readEvidence(verifyPath)
		if err != nil {
			return err
		}
		if err = c.verify(ctx, e); err != nil {
			return err
		}
		fmt.Println("workflow normal reopen persistence: PASS; only authenticated reads performed")
		return nil
	}
	if !filepath.IsAbs(createPath) || filepath.Clean(createPath) != createPath {
		return errors.New("evidence path must be absolute and clean")
	}
	f, err := os.OpenFile(createPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("a new qualification evidence file could not be created")
	}
	e := &evidence{SchemaVersion: 1, Runs: []wire.Run{}, Operations: []wire.OperationReceipt{}, AttemptedIDs: []string{}}
	qualificationErr := c.qualify(ctx, e)
	e.Complete = qualificationErr == nil
	data, encodeErr := json.MarshalIndent(e, "", "  ")
	if encodeErr == nil {
		data = append(data, '\n')
		_, encodeErr = f.Write(data)
	}
	if encodeErr == nil {
		encodeErr = f.Sync()
	}
	closeErr := f.Close()
	if encodeErr != nil || closeErr != nil {
		return errors.New("qualification evidence could not be persisted; inspect operations before another write")
	}
	if qualificationErr != nil {
		return qualificationErr
	}
	fmt.Printf("workflow normal HTTP qualification: PASS; workflow_id=%s; runs=%d; receipts=%d\n", e.Workflow.WorkflowId, len(e.Runs), len(e.Operations))
	return nil
}

func (c *client) qualify(ctx context.Context, e *evidence) (result error) {
	operation := func() string {
		id := uuid.NewString()
		e.AttemptedIDs = append(e.AttemptedIDs, id)
		return id
	}
	create := wire.CreateRequest{Name: "FEAT-153 正常文本资格 " + uuid.NewString()[:8], OperationId: operation()}
	var w wire.Workflow
	if err := c.write(ctx, "POST", "/v1/workflows", "CreateRequest", "Workflow", create, &w, 201, false); err != nil {
		return err
	}
	e.Workflow = w
	if err := c.receipt(ctx, e, create.OperationId, wire.Completed); err != nil {
		return err
	}
	base := "/v1/workflows/" + url.PathEscape(w.WorkflowId)
	if err := c.write(ctx, "POST", "/v1/workflow-local/editor-sessions", "EditorOpenRequest", "EditorSessionSecret", wire.EditorOpenRequest{WorkflowId: w.WorkflowId}, &c.session, 201, false); err != nil {
		return err
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var closed wire.CloseResult
		err := c.write(closeContext, "DELETE", "/v1/workflow-local/editor-sessions/"+url.PathEscape(c.session.SessionId), "", "CloseResult", nil, &closed, 200, false)
		c.session = wire.EditorSessionSecret{}
		if result == nil && (err != nil || !closed.Closed) {
			result = errors.New("qualification session did not close normally")
		}
	}()
	var bootstrap wire.Bootstrap
	if err := c.do(ctx, "GET", base+"/bootstrap", "", "Bootstrap", nil, &bootstrap, 200, true); err != nil {
		return err
	}
	if bootstrap.Workflow.WorkflowId != w.WorkflowId || bootstrap.Principal.OwnerId != domain.OwnerID || bootstrap.Principal.TenantId != domain.TenantID {
		return errors.New("normal bootstrap did not retain workflow and scope")
	}
	firstRevision := w.Revision
	save := wire.SaveRequest{Name: w.Name, Canvas: canvas("v1:"), ExpectedRevision: firstRevision, OperationId: operation()}
	if err := c.write(ctx, "PUT", base, "SaveRequest", "Workflow", save, &w, 200, true); err != nil {
		return err
	}
	e.Workflow = w
	if !w.Runnable || w.Revision == firstRevision || w.Canvas != save.Canvas {
		return errors.New("saved runnable graph did not receive its own revision")
	}
	var replay wire.Workflow
	if err := c.write(ctx, "PUT", base, "SaveRequest", "Workflow", save, &replay, 200, true); err != nil || replay.Revision != w.Revision {
		return errors.New("normal save replay did not retain the original revision")
	}
	if err := c.receipt(ctx, e, save.OperationId, wire.Completed); err != nil {
		return err
	}
	// An ordinary stale editor save is a CAS conflict, not an injected fault.
	stale := wire.SaveRequest{Name: w.Name, Canvas: save.Canvas, ExpectedRevision: firstRevision, OperationId: operation()}
	var conflict wire.ErrorResponse
	if err := c.write(ctx, "PUT", base, "SaveRequest", "ErrorResponse", stale, &conflict, 409, true); err != nil {
		return err
	}
	if conflict.Code != wire.RevisionConflict || conflict.OperationId == nil || *conflict.OperationId != stale.OperationId {
		return errors.New("ordinary stale revision did not preserve its rejected operation")
	}
	if err := c.receipt(ctx, e, stale.OperationId, wire.Rejected); err != nil {
		return err
	}
	for index, prefix := range []string{"v1:", "v2:"} {
		if index == 1 {
			save = wire.SaveRequest{Name: w.Name, Canvas: canvas(prefix), ExpectedRevision: w.Revision, OperationId: operation()}
			if err := c.write(ctx, "PUT", base, "SaveRequest", "Workflow", save, &w, 200, true); err != nil {
				return err
			}
			e.Workflow = w
			if err := c.receipt(ctx, e, save.OperationId, wire.Completed); err != nil {
				return err
			}
		}
		test := wire.TestRequest{ExpectedRevision: w.Revision, OperationId: operation(), Input: wire.TextInput{Input: "正常输入"}}
		var started wire.Run
		if err := c.write(ctx, "POST", base+"/test-runs", "TestRequest", "Run", test, &started, 202, true); err != nil {
			return err
		}
		finished, err := c.waitRun(ctx, base, started, prefix+test.Input.Input)
		if err != nil {
			return err
		}
		e.Runs = append(e.Runs, finished)
		if err = c.receipt(ctx, e, test.OperationId, wire.Completed); err != nil {
			return err
		}
		publish := wire.PublishRequest{ExpectedRevision: w.Revision, SuccessfulTestRunId: finished.RunId, OperationId: operation()}
		if err := c.write(ctx, "POST", base+"/versions", "PublishRequest", "Workflow", publish, &w, 200, true); err != nil {
			return err
		}
		e.Workflow = w
		if w.PublishedVersion == nil || *w.PublishedVersion != fmt.Sprintf("v0.0.%d", index+1) {
			return errors.New("normal publication did not allocate the expected patch version")
		}
		if err = c.receipt(ctx, e, publish.OperationId, wire.Completed); err != nil {
			return err
		}
		if err := c.write(ctx, "POST", base+"/versions", "PublishRequest", "Workflow", publish, &replay, 200, true); err != nil || replay.PublishedVersion == nil || *replay.PublishedVersion != *w.PublishedVersion {
			return errors.New("normal publication retry did not retain its original version")
		}
		if index == 1 {
			// A new operation on the same successfully tested revision is another
			// valid publication. This also covers real MySQL affected-row semantics.
			publish.OperationId = operation()
			if err := c.write(ctx, "POST", base+"/versions", "PublishRequest", "Workflow", publish, &w, 200, true); err != nil {
				return err
			}
			e.Workflow = w
			if w.PublishedVersion == nil || *w.PublishedVersion != "v0.0.3" || w.Revision != publish.ExpectedRevision {
				return errors.New("repeated publication changed revision or failed to advance its version")
			}
			if err = c.receipt(ctx, e, publish.OperationId, wire.Completed); err != nil {
				return err
			}
		}
	}
	for index, prefix := range []string{"v2:", "v1:"} {
		version := "v0.0.2"
		if index == 1 {
			version = "v0.0.1"
		}
		release := wire.RunRequest{Version: version, OperationId: operation(), Input: wire.TextInput{Input: "版本回读"}}
		var started wire.Run
		if err := c.write(ctx, "POST", base+"/runs", "RunRequest", "Run", release, &started, 202, false); err != nil {
			return err
		}
		finished, err := c.waitRun(ctx, base, started, prefix+release.Input.Input)
		if err != nil {
			return err
		}
		if finished.Version == nil || *finished.Version != version || finished.Mode != wire.Release {
			return errors.New("released execution did not retain its requested version")
		}
		e.Runs = append(e.Runs, finished)
		if err = c.receipt(ctx, e, release.OperationId, wire.Completed); err != nil {
			return err
		}
		var replayRun wire.Run
		if err := c.write(ctx, "POST", base+"/runs", "RunRequest", "Run", release, &replayRun, 202, false); err != nil || replayRun.RunId != finished.RunId {
			return errors.New("release retry did not keep the original execution")
		}
	}
	return c.verify(ctx, e)
}

func (c *client) receipt(ctx context.Context, e *evidence, id string, phase wire.OperationPhase) error {
	var receipt wire.OperationReceipt
	if err := c.get(ctx, "/v1/workflow-local/operations/"+id, "OperationReceipt", &receipt); err != nil {
		return err
	}
	if receipt.OperationId != id || receipt.Phase != phase {
		return errors.New("operation query did not preserve its original registration phase")
	}
	e.Operations = append(e.Operations, receipt)
	return nil
}

func (c *client) waitRun(ctx context.Context, base string, initial wire.Run, expected string) (wire.Run, error) {
	pollContext, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var r wire.Run
		if err := c.get(pollContext, base+"/runs/"+url.PathEscape(initial.RunId), "Run", &r); err != nil {
			return r, err
		}
		if r.RunId != initial.RunId || r.OperationId != initial.OperationId || r.WorkflowId != initial.WorkflowId {
			return r, errors.New("execution identity changed during normal polling")
		}
		if r.Terminal {
			if r.State != "succeeded" || r.Output == nil || *r.Output != expected || r.Nodes == nil || len(*r.Nodes) != 3 {
				return r, errors.New("native execution did not produce the expected three-node text result")
			}
			return r, nil
		}
		select {
		case <-pollContext.Done():
			return r, errors.New("execution remains pending; inspect the recorded operation before another write")
		case <-ticker.C:
		}
	}
}

func (c *client) verify(ctx context.Context, e *evidence) error {
	base := "/v1/workflows/" + url.PathEscape(e.Workflow.WorkflowId)
	var w wire.Workflow
	if err := c.get(ctx, base, "Workflow", &w); err != nil {
		return err
	}
	if !reflect.DeepEqual(w, e.Workflow) {
		return errors.New("workflow content changed during normal persistence readback")
	}
	found := false
	workflowCursor := ""
	for page := 0; page < 20 && !found; page++ {
		query := url.Values{"limit": {"50"}}
		if workflowCursor != "" {
			query.Set("cursor", workflowCursor)
		}
		var list wire.WorkflowList
		if err := c.get(ctx, "/v1/workflows?"+query.Encode(), "WorkflowList", &list); err != nil {
			return err
		}
		for _, item := range list.Items {
			if item.WorkflowId == w.WorkflowId {
				if item.Revision != w.Revision || item.Name != w.Name || item.Runnable != w.Runnable || !reflect.DeepEqual(item.PublishedVersion, w.PublishedVersion) {
					return errors.New("workflow list summary differs from its persisted detail")
				}
				found = true
			}
		}
		if list.NextCursor == nil {
			break
		}
		workflowCursor = *list.NextCursor
	}
	if !found {
		return errors.New("normal workflow list is missing the qualified workflow")
	}
	for _, expected := range e.Runs {
		var r wire.Run
		if err := c.get(ctx, base+"/runs/"+url.PathEscape(expected.RunId), "Run", &r); err != nil {
			return err
		}
		if !reflect.DeepEqual(r, expected) {
			return errors.New("native run history changed during normal persistence readback")
		}
	}
	for _, expected := range e.Operations {
		var receipt wire.OperationReceipt
		if err := c.get(ctx, "/v1/workflow-local/operations/"+expected.OperationId, "OperationReceipt", &receipt); err != nil {
			return err
		}
		if !reflect.DeepEqual(receipt, expected) {
			return errors.New("operation receipt changed during normal persistence readback")
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		var list wire.RunList
		query := url.Values{"limit": {"1"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		if err := c.get(ctx, base+"/runs?"+query.Encode(), "RunList", &list); err != nil {
			return err
		}
		for _, item := range list.Items {
			if seen[item.RunId] {
				return errors.New("normal history pagination repeated an execution")
			}
			seen[item.RunId] = true
		}
		if list.NextCursor == nil {
			break
		}
		cursor = *list.NextCursor
	}
	if len(seen) != len(e.Runs) {
		return errors.New("normal history does not match the registered executions")
	}
	for _, r := range e.Runs {
		if !seen[r.RunId] {
			return errors.New("normal history is missing an expected execution")
		}
	}
	return nil
}

func (c *client) get(ctx context.Context, path, schema string, out any) error {
	return c.do(ctx, "GET", path, "", schema, nil, out, 200, false)
}
func (c *client) write(ctx context.Context, method, path, inSchema, outSchema string, in, out any, status int, editor bool) error {
	return c.do(ctx, method, path, inSchema, outSchema, in, out, status, editor)
}
func (c *client) do(ctx context.Context, method, path, inSchema, outSchema string, in, out any, status int, editor bool) error {
	c.lastStatus = 0
	var data []byte
	if in != nil {
		var err error
		data, err = json.Marshal(in)
		if err != nil || len(data) > domain.MessageBytes || transport.Validate(inSchema, data) != nil {
			return errors.New("qualification request does not conform to its consumed source")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:18888"+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("qualification request could not be prepared")
	}
	req.Header.Set("Authorization", "Bearer "+c.credential.Token)
	req.Header.Set("X-Yijie-Run-Epoch", c.credential.RunEpoch)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if editor {
		req.Header.Set("X-Yijie-Editor-Session", c.session.Secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("qualification HTTP response is unavailable; inspect attempted operation IDs before retrying")
	}
	defer resp.Body.Close()
	c.lastStatus = resp.StatusCode
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
		return errors.New("qualification response headers do not match the local HTTP boundary")
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, domain.MessageBytes+1))
	if err != nil || len(data) > domain.MessageBytes {
		return errors.New("qualification response exceeded its readable boundary")
	}
	if resp.StatusCode != status {
		var failure wire.ErrorResponse
		if transport.Validate("ErrorResponse", data) == nil && json.Unmarshal(data, &failure) == nil {
			return fmt.Errorf("qualification %s returned %s; inspect attempted operation IDs before retrying", outSchema, failure.Code)
		}
		return errors.New("qualification response status does not match the consumed source")
	}
	if transport.Validate(outSchema, data) != nil || json.Unmarshal(data, out) != nil {
		return errors.New("qualification response does not conform to its consumed source")
	}
	return nil
}

func readEvidence(path string) (*evidence, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("evidence path must be absolute and clean")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("qualification artifact is unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, domain.MessageBytes+1))
	var e evidence
	if err != nil || len(data) > domain.MessageBytes || json.Unmarshal(data, &e) != nil || e.SchemaVersion != 1 || !e.Complete || len(e.Runs) != 4 || len(e.Operations) == 0 {
		return nil, errors.New("qualification artifact is incomplete or invalid")
	}
	validate := func(schema string, value any) bool {
		data, err := json.Marshal(value)
		return err == nil && transport.Validate(schema, data) == nil
	}
	if !validate("Workflow", e.Workflow) {
		return nil, errors.New("qualification workflow artifact does not conform to its source")
	}
	for _, r := range e.Runs {
		if !validate("Run", r) || r.WorkflowId != e.Workflow.WorkflowId {
			return nil, errors.New("qualification run artifact does not conform to its source")
		}
	}
	for _, r := range e.Operations {
		if !validate("OperationReceipt", r) {
			return nil, errors.New("qualification operation artifact does not conform to its source")
		}
	}
	return &e, nil
}

// This is a normal native Coze canvas fixture, not a second editable wire DTO.
func canvas(prefix string) string {
	ref := func(block, name string) any {
		return map[string]any{"type": "string", "value": map[string]any{"type": "ref", "content": map[string]any{"source": "block-output", "blockID": block, "name": name}}}
	}
	start := map[string]any{"id": "100001", "type": "1", "data": map[string]any{"nodeMeta": map[string]any{"title": "开始"}, "outputs": []any{map[string]any{"name": "input", "type": "string", "required": true}}}}
	text := map[string]any{"id": "200001", "type": "15", "data": map[string]any{"nodeMeta": map[string]any{"title": "文本拼接"}, "outputs": []any{map[string]any{"name": "output", "type": "string"}}, "inputs": map[string]any{"method": "concat", "inputParameters": []any{map[string]any{"name": "input", "input": ref("100001", "input")}}, "concatParams": []any{map[string]any{"name": "concatResult", "input": map[string]any{"type": "string", "value": map[string]any{"type": "literal", "content": prefix + "{{input}}"}}}}}}}
	end := map[string]any{"id": "900001", "type": "2", "data": map[string]any{"nodeMeta": map[string]any{"title": "结束"}, "inputs": map[string]any{"terminatePlan": "returnVariables", "inputParameters": []any{map[string]any{"name": "result", "input": ref("200001", "output")}}}}}
	data, _ := json.Marshal(map[string]any{"nodes": []any{start, text, end}, "edges": []any{map[string]any{"sourceNodeID": "100001", "targetNodeID": "200001"}, map[string]any{"sourceNodeID": "200001", "targetNodeID": "900001"}}, "versions": map[string]any{"loop": "v2"}})
	return string(data)
}
