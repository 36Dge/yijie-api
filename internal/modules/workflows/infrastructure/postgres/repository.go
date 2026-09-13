package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }
func storeError() error                  { return domain.Fail("storage_unavailable") }
func (r *Repository) Ping(ctx context.Context) error {
	if r.pool.Ping(ctx) != nil {
		return storeError()
	}
	return nil
}
func (r *Repository) RequireCurrent(ctx context.Context) error {
	var version int64
	err := r.pool.QueryRow(ctx, `SELECT version_id FROM workflow_schema_version WHERE is_applied ORDER BY id DESC LIMIT 1`).Scan(&version)
	if err != nil || version != 1 {
		return storeError()
	}
	var tables bool
	err = r.pool.QueryRow(ctx, `SELECT to_regclass('workflow_scope_bindings') IS NOT NULL AND to_regclass('workflow_resources') IS NOT NULL AND to_regclass('workflow_operations') IS NOT NULL AND to_regclass('workflow_audit') IS NOT NULL`).Scan(&tables)
	if err != nil || !tables {
		return storeError()
	}
	return nil
}
func (r *Repository) Principal(ctx context.Context) (domain.Principal, error) {
	var p domain.Principal
	err := r.pool.QueryRow(ctx, `SELECT owner_user_id::text,tenant_id::text,coze_user_id,coze_space_id FROM workflow_scope_bindings WHERE scope_key=$1`, domain.ScopeKey).Scan(&p.OwnerID, &p.TenantID, &p.UserID, &p.SpaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, domain.Fail("resource_not_found")
	}
	if err != nil {
		return p, storeError()
	}
	return p, nil
}
func (r *Repository) BindPrincipal(ctx context.Context, p domain.Principal) error {
	if p.OwnerID != domain.OwnerID || p.TenantID != domain.TenantID || p.UserID == "" || p.SpaceID == "" {
		return domain.Fail("protocol_mismatch")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storeError()
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO workflow_scope_bindings(scope_key,owner_user_id,tenant_id,coze_user_id,coze_space_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(scope_key) DO NOTHING`, domain.ScopeKey, p.OwnerID, p.TenantID, p.UserID, p.SpaceID)
	if err != nil {
		return storeError()
	}
	var user, space, owner, tenant string
	err = tx.QueryRow(ctx, `SELECT coze_user_id,coze_space_id,owner_user_id::text,tenant_id::text FROM workflow_scope_bindings WHERE scope_key=$1 FOR UPDATE`, domain.ScopeKey).Scan(&user, &space, &owner, &tenant)
	if err != nil {
		return storeError()
	}
	if user != p.UserID || space != p.SpaceID || owner != p.OwnerID || tenant != p.TenantID {
		return domain.Fail("protocol_mismatch")
	}
	if audit(ctx, tx, "principal", "", "", "success") != nil {
		return storeError()
	}
	if tx.Commit(ctx) != nil {
		return storeError()
	}
	return nil
}
func (r *Repository) BindResources(ctx context.Context, resources []domain.Workflow) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storeError()
	}
	defer tx.Rollback(ctx)
	for _, w := range resources {
		if _, err = tx.Exec(ctx, `INSERT INTO workflow_resources(workflow_id,scope_key) VALUES($1,$2) ON CONFLICT(workflow_id) DO NOTHING`, w.ID, domain.ScopeKey); err != nil {
			return storeError()
		}
	}
	if tx.Commit(ctx) != nil {
		return storeError()
	}
	return nil
}
func (r *Repository) HasResource(ctx context.Context, id string) (bool, error) {
	var found bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_resources WHERE scope_key=$1 AND workflow_id=$2)`, domain.ScopeKey, id).Scan(&found)
	if err != nil {
		return false, storeError()
	}
	return found, nil
}

func (r *Repository) Claim(ctx context.Context, in domain.Intent) (domain.Receipt, bool, error) {
	receipt := domain.Receipt{OperationID: in.ID, Kind: in.Kind, Phase: "recorded", WorkflowID: in.WorkflowID}
	data, _ := json.Marshal(receipt)
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return receipt, false, storeError()
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO workflow_operations(operation_id,scope_key,kind,request_sha256,workflow_id,phase,receipt) VALUES($1,$2,$3,$4,NULLIF($5,''),'recorded',$6) ON CONFLICT(operation_id) DO NOTHING`, in.ID, domain.ScopeKey, in.Kind, in.Hash, in.WorkflowID, data)
	if err != nil {
		return receipt, false, storeError()
	}
	fresh := tag.RowsAffected() == 1
	if !fresh {
		var hash, kind, workflow string
		err = tx.QueryRow(ctx, `SELECT request_sha256,kind,COALESCE(workflow_id,''),receipt FROM workflow_operations WHERE scope_key=$1 AND operation_id=$2`, domain.ScopeKey, in.ID).Scan(&hash, &kind, &workflow, &data)
		if err != nil {
			return receipt, false, storeError()
		}
		// create gains a resource only after its first successful dispatch.
		if hash != in.Hash || kind != in.Kind || (in.Kind != "create" && workflow != in.WorkflowID) {
			return receipt, false, domain.Fail("operation_conflict")
		}
		if json.Unmarshal(data, &receipt) != nil {
			return receipt, false, storeError()
		}
	} else if audit(ctx, tx, in.Kind, in.ID, in.WorkflowID, "recorded") != nil {
		return receipt, false, storeError()
	}
	if tx.Commit(ctx) != nil {
		return receipt, false, storeError()
	}
	return receipt, fresh, nil
}
func (r *Repository) ReadReceipt(ctx context.Context, id string) (domain.Receipt, error) {
	var receipt domain.Receipt
	var data []byte
	err := r.pool.QueryRow(ctx, `SELECT receipt FROM workflow_operations WHERE scope_key=$1 AND operation_id=$2`, domain.ScopeKey, id).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return receipt, domain.Fail("resource_not_found")
	}
	if err != nil || json.Unmarshal(data, &receipt) != nil {
		return receipt, storeError()
	}
	return receipt, nil
}
func (r *Repository) Finish(ctx context.Context, receipt domain.Receipt) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storeError()
	}
	defer tx.Rollback(ctx)
	var before domain.Receipt
	var data []byte
	err = tx.QueryRow(ctx, `SELECT receipt FROM workflow_operations WHERE scope_key=$1 AND operation_id=$2 FOR UPDATE`, domain.ScopeKey, receipt.OperationID).Scan(&data)
	if err != nil || json.Unmarshal(data, &before) != nil {
		return storeError()
	}
	if before.Kind != receipt.Kind || (before.WorkflowID != "" && receipt.WorkflowID != "" && before.WorkflowID != receipt.WorkflowID) {
		return domain.Fail("protocol_mismatch")
	}
	if before.Phase == "completed" || before.Phase == "rejected" {
		return nil
	}
	if receipt.WorkflowID != "" {
		_, err = tx.Exec(ctx, `INSERT INTO workflow_resources(workflow_id,scope_key) VALUES($1,$2) ON CONFLICT(workflow_id) DO NOTHING`, receipt.WorkflowID, domain.ScopeKey)
		if err != nil {
			return storeError()
		}
	}
	data, err = json.Marshal(receipt)
	if err != nil {
		return storeError()
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_operations SET workflow_id=NULLIF($3,''),phase=$4,receipt=$5,updated_at=now() WHERE scope_key=$1 AND operation_id=$2`, domain.ScopeKey, receipt.OperationID, receipt.WorkflowID, receipt.Phase, data)
	if err != nil {
		return storeError()
	}
	if audit(ctx, tx, receipt.Kind, receipt.OperationID, receipt.WorkflowID, receipt.Phase) != nil {
		return storeError()
	}
	if tx.Commit(ctx) != nil {
		return storeError()
	}
	return nil
}

// audit uses concrete pgx.Tx so no SQL or transaction semantics escape infrastructure.
func audit(ctx context.Context, tx pgx.Tx, action, id, resource, code string) error {
	_, err := tx.Exec(ctx, `INSERT INTO workflow_audit(scope_key,operation_id,workflow_id,action,outcome_code) VALUES($1,NULLIF($2,'')::uuid,NULLIF($3,''),$4,$5)`, domain.ScopeKey, id, resource, action, code)
	return err
}
func (r *Repository) Audit(ctx context.Context, action, resource, code string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO workflow_audit(scope_key,workflow_id,action,outcome_code) VALUES($1,NULLIF($2,''),$3,$4)`, domain.ScopeKey, resource, action, code)
	if err != nil {
		return storeError()
	}
	return nil
}
