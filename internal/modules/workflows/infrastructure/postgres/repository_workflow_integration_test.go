//go:build workflowintegration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/36Dge/yijie-api/internal/workflowlocal"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This test requires Infra's explicit migration and normal API principal setup.
// It never migrates, clears tables, changes audit rows, or claims engine success.
func TestWorkflowPostgresNormalPersistence(t *testing.T) {
	if os.Getenv("YIJIE_WORKFLOW_PG_QUALIFICATION") != "normal" {
		t.Fatal("explicit workflow PostgreSQL qualification gate is required")
	}
	config, err := workflowlocal.LoadConfig()
	if err != nil {
		t.Fatal("dedicated workflow configuration is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openQualificationPool(t, ctx, config.DSN)
	defer pool.Close()
	repo := New(pool)
	check := func(stage string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s did not complete", stage)
		}
	}
	check("explicit migration prerequisite", repo.RequireCurrent(ctx))
	var database string
	check("database identity query", pool.QueryRow(ctx, "SELECT current_database()").Scan(&database))
	if database != "yijie_workflow_local" {
		t.Fatal("qualification is not connected to the dedicated database")
	}
	var applied int
	check("migration book query", pool.QueryRow(ctx, "SELECT count(*) FROM workflow_schema_version WHERE is_applied AND version_id=1").Scan(&applied))
	if applied != 1 {
		t.Fatal("explicit workflow migration is not uniquely recorded")
	}
	principal, err := repo.Principal(ctx)
	check("real principal read", err)
	if principal.OwnerID != domain.OwnerID || principal.TenantID != domain.TenantID || principal.UserID == "" || principal.SpaceID == "" {
		t.Fatal("the normally initialized principal is inconsistent")
	}
	check("normal repeated principal bind", repo.BindPrincipal(ctx, principal))
	workflowID := os.Getenv("YIJIE_WORKFLOW_QUALIFICATION_WORKFLOW_ID")
	if workflowID == "" || len(workflowID) > 64 || strings.ContainsAny(workflowID, "/\\\r\n") {
		t.Fatal("HTTP qualification workflow ID is required")
	}
	bound, err := repo.HasResource(ctx, workflowID)
	check("normal resource association read", err)
	if !bound {
		t.Fatal("HTTP qualification workflow is not bound to this scope")
	}
	check("normal repeated resource bind", repo.BindResources(ctx, []domain.Workflow{{ID: workflowID}}))
	var scope, owner, tenant string
	check("scope association query", pool.QueryRow(ctx, `SELECT r.scope_key,b.owner_user_id::text,b.tenant_id::text FROM workflow_resources r JOIN workflow_scope_bindings b ON b.scope_key=r.scope_key WHERE r.workflow_id=$1`, workflowID).Scan(&scope, &owner, &tenant))
	if scope != domain.ScopeKey || owner != domain.OwnerID || tenant != domain.TenantID {
		t.Fatal("resource association does not retain the fixed scope")
	}
	var triggerDefinition, functionDefinition string
	check("append-only trigger inspection", pool.QueryRow(ctx, `SELECT pg_get_triggerdef(t.oid),pg_get_functiondef(t.tgfoid) FROM pg_trigger t WHERE t.tgrelid='workflow_audit'::regclass AND t.tgname='workflow_audit_append_only' AND NOT t.tgisinternal AND t.tgenabled='O'`).Scan(&triggerDefinition, &functionDefinition))
	if !strings.Contains(triggerDefinition, "BEFORE DELETE OR UPDATE") && !strings.Contains(triggerDefinition, "BEFORE UPDATE OR DELETE") {
		t.Fatal("audit trigger does not cover update and delete")
	}
	if !strings.Contains(functionDefinition, "RAISE EXCEPTION 'workflow audit is append-only'") {
		t.Fatal("audit mutation guard definition is inconsistent")
	}
	operationID := uuid.NewString()
	digest := sha256.Sum256([]byte("FEAT-153 declared storage qualification; no engine dispatch; " + operationID))
	intent := domain.Intent{ID: operationID, Kind: "save", WorkflowID: workflowID, Hash: hex.EncodeToString(digest[:])}
	type claimResult struct {
		receipt domain.Receipt
		fresh   bool
		err     error
	}
	results := make(chan claimResult, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			r, fresh, e := repo.Claim(ctx, intent)
			results <- claimResult{r, fresh, e}
		}()
	}
	group.Wait()
	close(results)
	freshCount := 0
	for result := range results {
		check("concurrent normal receipt claim", result.err)
		if result.receipt.OperationID != operationID || result.receipt.Phase != "recorded" {
			t.Fatal("concurrent receipt readback differs")
		}
		if result.fresh {
			freshCount++
		}
	}
	if freshCount != 1 {
		t.Fatal("concurrent claim did not allocate exactly one durable intent")
	}
	// Deliberately labelled storage probe, never a fake completed execution.
	receipt := domain.Receipt{OperationID: operationID, Kind: "save", WorkflowID: workflowID, Phase: "unknown", ErrorCode: "operation_unknown"}
	check("normal unknown receipt persistence", repo.Finish(ctx, receipt))
	check("normal qualification audit append", repo.Audit(ctx, "storage_qualification", workflowID, "normal_readback"))
	var phases []string
	rows, err := pool.Query(ctx, `SELECT outcome_code FROM workflow_audit WHERE scope_key=$1 AND operation_id=$2 ORDER BY id`, domain.ScopeKey, operationID)
	check("normal audit read", err)
	for rows.Next() {
		var phase string
		check("audit row decode", rows.Scan(&phase))
		phases = append(phases, phase)
	}
	check("audit cursor completion", rows.Err())
	rows.Close()
	if len(phases) != 2 || phases[0] != "recorded" || phases[1] != "unknown" {
		t.Fatal("receipt and audit sequence did not persist together")
	}
	pool.Close()
	pool = openQualificationPool(t, ctx, config.DSN)
	defer pool.Close()
	reopened := New(pool)
	got, err := reopened.ReadReceipt(ctx, operationID)
	check("receipt after normal connection reopen", err)
	if got != receipt {
		t.Fatal("receipt did not survive connection reopen")
	}
	again, err := reopened.Principal(ctx)
	check("principal after normal connection reopen", err)
	if again != principal {
		t.Fatal("principal changed after connection reopen")
	}
	bound, err = reopened.HasResource(ctx, workflowID)
	check("resource after normal connection reopen", err)
	if !bound {
		t.Fatal("resource association did not survive connection reopen")
	}
	t.Log("explicit schema, fixed scope, eight concurrent claims, unknown receipt, normal audit, and connection reopen: PASS; no engine success asserted")
}

func openQualificationPool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil || pool.Ping(ctx) != nil {
		if pool != nil {
			pool.Close()
		}
		t.Fatal("dedicated PostgreSQL connection is unavailable")
	}
	return pool
}
