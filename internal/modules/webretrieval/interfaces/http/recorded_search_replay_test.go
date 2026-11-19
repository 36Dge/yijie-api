package webhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/webretrieval/application"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/36Dge/yijie-api/internal/modules/webretrieval/infrastructure/postgres"
	"github.com/google/uuid"
)

type recordedRow struct {
	Args map[string]any  `json:"args"`
	Body json.RawMessage `json:"body"`
}
type recordPeer struct {
	repo   d.Repository
	bodies map[string]json.RawMessage
	sends  atomic.Int32
}

func (e *recordPeer) Execute(ctx context.Context, o d.Operation, q d.Request) ([]byte, error) {
	var args map[string]any
	_ = json.Unmarshal(q.Arguments, &args)
	key, _ := json.Marshal(args)
	body := e.bodies[string(key)]
	if len(body) == 0 {
		return nil, fmt.Errorf("no recorded supplier response")
	}
	var data struct {
		Results   []json.RawMessage
		Truncated bool
		Dropped   int `json:"dropped_results"`
		Error     *struct{ Code string }
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	claim, err := e.repo.Claim(ctx, o.ID, o.Digest, uuid.New())
	if err != nil {
		return nil, err
	}
	e.sends.Add(1)
	outcome, state, dispatch := "results", "completed", "completed"
	if data.Error != nil {
		outcome, state, dispatch = "failed", "unknown", "may_have_dispatched"
		claim.Operation.Facts.ErrorCode = d.Code(data.Error.Code)
	} else if data.Truncated || data.Dropped > 0 {
		outcome = "partial"
	} else if len(data.Results) == 0 {
		outcome = "empty"
	}
	claim.Operation.Facts.Transport = "responded"
	claim.Operation.Facts.Outcome = outcome
	return body, e.repo.Finish(ctx, claim, state, dispatch)
}
func TestRecordedHangzhouScopedReplay(t *testing.T) {
	audit := os.Getenv("SCOPED_REPLAY_DIRECTORY")
	if audit == "" || os.Getenv("SCOPED_REPLAY_RUST_TEST") == "" {
		t.Skip("requires fixed recorded responses, an isolated test DB, and the local gateway test binary")
	}
	raw, err := os.ReadFile(filepath.Join(audit, "recorded.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inputs map[string][]recordedRow
	if json.Unmarshal(raw, &inputs) != nil {
		t.Fatal("invalid recording")
	}
	pool, principal := isolatedWebPrincipal(t)
	repo, err := postgres.New(pool, 250)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"three", "kimi", "minimax"} {
		t.Run(name, func(t *testing.T) {
			grant, err := repo.Mint(context.Background(), principal, uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			rows := inputs[name]
			peer := &recordPeer{repo: repo, bodies: map[string]json.RawMessage{}}
			scope := uuid.New()
			for _, v := range rows {
				key, _ := json.Marshal(v.Args)
				if _, ok := peer.bodies[string(key)]; !ok {
					peer.bodies[string(key)] = v.Body
				}
			}
			mux := http.NewServeMux()
			(&Handler{Service: application.New(repo, peer)}).Register(mux)
			mux.HandleFunc("POST /admit", func(w http.ResponseWriter, r *http.Request) {
				var p map[string]any
				_ = json.NewDecoder(r.Body).Decode(&p)
				_ = json.NewEncoder(w).Encode(map[string]any{"scope_id": scope, "root_thread_id": p["thread_id"], "root_turn_id": p["turn_id"], "operation_id": p["operation_id"], "deadline_unix_ms": time.Now().Add(45 * time.Second).UnixMilli()})
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			cmd := exec.Command(os.Getenv("SCOPED_REPLAY_RUST_TEST"), "recorded_scoped_calls_from_file", "--ignored", "--test-threads=1", "--nocapture")
			cmd.Env = append(os.Environ(), "SCOPED_REPLAY_ORIGIN="+server.URL, "SCOPED_REPLAY_GRANT="+grant.Bearer, "SCOPED_REPLAY_SET="+name)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("gateway replay: %s", output)
			}
			expected := map[string]int{"three": 3, "kimi": 10, "minimax": 19}[name]
			if int(peer.sends.Load()) != expected {
				t.Errorf("dispatches=%d expected=%d", peer.sends.Load(), expected)
			}
			t.Logf("logical=%d physical=%d", len(rows), peer.sends.Load())
		})
	}
}
