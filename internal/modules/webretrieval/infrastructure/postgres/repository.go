package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	auth "github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	authpg "github.com/36Dge/yijie-api/internal/modules/authorization/infrastructure/postgres"
	tenancy "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	d "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool       *pgxpool.Pool
	dailyLimit int
}

func New(pool *pgxpool.Pool, dailyLimit int) (*Repository, error) {
	if pool == nil || dailyLimit < 1 || dailyLimit > 100000 {
		return nil, d.Invalid
	}
	return &Repository{pool: pool, dailyLimit: dailyLimit}, nil
}
func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func authorize(ctx context.Context, tx pgx.Tx, scope d.Scope) error {
	err := authpg.LockPermission(ctx, tx, tenancy.Scope{UserID: scope.User, TenantID: scope.Tenant, AuthorizationRevision: scope.Revision}, auth.WebRetrieve)
	if errors.Is(err, auth.ErrDenied) {
		return d.Denied
	}
	return err
}
func (r *Repository) Mint(ctx context.Context, scope d.Scope, native uuid.UUID) (d.Grant, error) {
	if native == uuid.Nil {
		return d.Grant{}, d.Invalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return d.Grant{}, err
	}
	defer tx.Rollback(ctx)
	if err = authorize(ctx, tx, scope); err != nil {
		return d.Grant{}, err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return d.Grant{}, err
	}
	g := d.Grant{Binding: d.Binding{ID: uuid.New(), Scope: scope}, ID: uuid.New(), Bearer: hex.EncodeToString(secret)}
	err = tx.QueryRow(ctx, `INSERT INTO web_bindings (id,tenant_id,user_id,native_reference,authorization_revision,credential_revision)
	 VALUES ($1,$2,$3,$4,$5,1) ON CONFLICT (tenant_id,user_id,native_reference) DO UPDATE
	 SET credential_revision=web_bindings.credential_revision+1, authorization_revision=EXCLUDED.authorization_revision
	 WHERE web_bindings.revoked_at IS NULL RETURNING id,credential_revision`, g.Binding.ID, scope.Tenant, scope.User, native, scope.Revision).Scan(&g.Binding.ID, &g.CredentialRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return d.Grant{}, d.Denied
	}
	if err != nil {
		return d.Grant{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO web_grants(id,binding_id,bearer_sha256,expires_at) VALUES($1,$2,$3,now()+interval '300 seconds') RETURNING expires_at`, g.ID, g.Binding.ID, digest(g.Bearer)).Scan(&g.Expires)
	if err != nil {
		return d.Grant{}, err
	}
	return g, tx.Commit(ctx)
}
func (r *Repository) Resolve(ctx context.Context, bearer string) (d.Binding, error) {
	if len(bearer) != 64 {
		return d.Binding{}, d.Expired
	}
	var b d.Binding
	err := r.pool.QueryRow(ctx, `SELECT b.id,b.tenant_id,b.user_id,b.authorization_revision FROM web_grants g JOIN web_bindings b ON b.id=g.binding_id WHERE g.bearer_sha256=$1 AND g.expires_at>now() AND b.revoked_at IS NULL`, digest(bearer)).Scan(&b.ID, &b.Scope.Tenant, &b.Scope.User, &b.Scope.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, d.Expired
	}
	return b, err
}
func (r *Repository) FindBinding(ctx context.Context, scope d.Scope, id uuid.UUID) (d.Binding, error) {
	var b d.Binding
	err := r.pool.QueryRow(ctx, `SELECT id,tenant_id,user_id,authorization_revision FROM web_bindings WHERE id=$1 AND tenant_id=$2 AND user_id=$3`, id, scope.Tenant, scope.User).Scan(&b.ID, &b.Scope.Tenant, &b.Scope.User, &b.Scope.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, d.Denied
	}
	return b, err
}
func (r *Repository) GetInternal(ctx context.Context, id uuid.UUID) (d.Operation, error) {
	return scan(r.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.id=$1`, id))
}
func binding(ctx context.Context, tx pgx.Tx, b d.Binding) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT revoked_at IS NULL AND authorization_revision=$4 FROM web_bindings WHERE id=$1 AND tenant_id=$2 AND user_id=$3 FOR UPDATE`, b.ID, b.Scope.Tenant, b.Scope.User, b.Scope.Revision).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return d.Denied
	}
	return err
}

const operationColumns = `o.id,o.binding_id,b.tenant_id,b.user_id,b.authorization_revision,o.parameter_digest,o.tool,o.state,o.dispatch,o.deadline,COALESCE(o.claim_id,'00000000-0000-0000-0000-000000000000'),COALESCE(o.executor_instance,'00000000-0000-0000-0000-000000000000'),COALESCE(o.search_scope,'00000000-0000-0000-0000-000000000000'),COALESCE(o.admitted_revision,0),o.transport_fact,o.search_outcome,COALESCE(o.search_error,'')`

func scan(row pgx.Row) (d.Operation, error) {
	var o d.Operation
	err := row.Scan(&o.ID, &o.Binding.ID, &o.Binding.Scope.Tenant, &o.Binding.Scope.User, &o.Binding.Scope.Revision, &o.Digest, &o.Tool, &o.State, &o.Dispatch, &o.Deadline, &o.ClaimID, &o.Executor, &o.SearchScope, &o.Revision, &o.Facts.Transport, &o.Facts.Outcome, &o.Facts.ErrorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, d.Denied
	}
	return o, err
}
func event(ctx context.Context, tx pgx.Tx, o d.Operation) error {
	_, err := tx.Exec(ctx, `INSERT INTO web_operation_events(operation_id,binding_id,state,dispatch,transport_fact,search_outcome) VALUES($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''))`, o.ID, o.Binding.ID, o.State, o.Dispatch, o.Facts.Transport, o.Facts.Outcome)
	return err
}
func (r *Repository) Admit(ctx context.Context, b d.Binding, request d.Request) (d.Operation, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return d.Operation{}, false, err
	}
	defer tx.Rollback(ctx)
	if err = authorize(ctx, tx, b.Scope); err != nil {
		return d.Operation{}, false, err
	}
	if err = binding(ctx, tx, b); err != nil {
		return d.Operation{}, false, err
	}
	o, err := scan(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.id=COALESCE((SELECT operation_id FROM web_search_call_refs WHERE call_id=$1),$1) AND b.id=$2 AND b.tenant_id=$3 AND b.user_id=$4`, request.ID, b.ID, b.Scope.Tenant, b.Scope.User))
	if err == nil {
		if o.Binding.ID != b.ID || o.Digest != request.Digest || o.SearchScope != request.SearchScope || (o.SearchScope != uuid.Nil && o.Revision != b.Scope.Revision) {
			return o, false, d.Conflict
		}
		return o, false, nil
	}
	if !errors.Is(err, d.Denied) {
		return o, false, err
	}
	// The tenant and binding are locked above. Resolve the original before
	// freshness, quota or concurrency checks; only a fresh search consumes them.
	if request.SearchScope != uuid.Nil {
		o, err = scan(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.binding_id=$1 AND o.admitted_revision=$2 AND o.search_scope=$3 AND o.parameter_digest=$4 AND NOT o.retryable_before_dispatch`, b.ID, b.Scope.Revision, request.SearchScope, request.Digest))
		if err == nil {
			if o.State != "ready" || o.Deadline.After(time.Now()) {
				if err = bindSearchCall(ctx, tx, request.ID, o.ID); err != nil {
					return o, false, err
				}
				return o, false, tx.Commit(ctx)
			}
			// A stale, never-claimed admission is released in this transaction.
			// Claims use the same binding lock. Readers keep the original ID.
			if err = releaseReady(ctx, tx, &o, "timeout"); err != nil {
				return o, false, err
			}
		} else if !errors.Is(err, d.Denied) {
			return o, false, err
		}
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return o, false, err
	}
	if request.ID.Version() != 7 {
		return o, false, d.Invalid
	}
	sec, nsec := request.ID.Time().UnixTime()
	created := time.Unix(sec, nsec)
	if now.Sub(created) > 5*time.Minute || created.Sub(now) > time.Minute {
		return o, false, d.OldOperation
	}
	var used, active int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE o.created_at>=date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), count(*) FILTER (WHERE b.user_id=$2 AND o.state IN ('ready','dispatch_committed') AND o.deadline>now()) FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE b.tenant_id=$1`, b.Scope.Tenant, b.Scope.User).Scan(&used, &active)
	if err != nil {
		return o, false, err
	}
	if used >= r.dailyLimit {
		return o, false, d.Quota
	}
	if active >= wire.SubjectConcurrency {
		return o, false, d.Busy
	}
	o = d.Operation{ID: request.ID, Tool: request.Tool, Binding: b, Digest: request.Digest, State: "ready", Dispatch: "not_dispatched", Deadline: now.Add(45 * time.Second).Truncate(time.Millisecond)}
	var scope, revision any
	if request.SearchScope != uuid.Nil {
		if !request.Deadline.After(now) {
			return d.Operation{}, false, d.Code("timeout")
		}
		if request.Deadline.Before(o.Deadline) {
			o.Deadline = request.Deadline.Truncate(time.Millisecond)
		}
		o.SearchScope, o.Revision = request.SearchScope, b.Scope.Revision
		scope, revision = o.SearchScope, o.Revision
	}
	tag, err := tx.Exec(ctx, `INSERT INTO web_operations(id,binding_id,parameter_digest,tool,state,dispatch,deadline,search_scope,admitted_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(id) DO NOTHING`, o.ID, b.ID, o.Digest, request.Tool, o.State, o.Dispatch, o.Deadline, scope, revision)
	if err != nil {
		return o, false, err
	}
	if tag.RowsAffected() != 1 {
		return o, false, d.Conflict
	}
	if request.SearchScope != uuid.Nil {
		if err = bindSearchCall(ctx, tx, request.ID, o.ID); err != nil {
			return o, false, err
		}
	}
	if err = event(ctx, tx, o); err != nil {
		return o, false, err
	}
	err = tx.Commit(ctx)
	return o, err == nil, err
}

func bindSearchCall(ctx context.Context, tx pgx.Tx, call, operation uuid.UUID) error {
	result, err := tx.Exec(ctx, `INSERT INTO web_search_call_refs(call_id,operation_id) VALUES($1,$2)
      ON CONFLICT(call_id) DO UPDATE SET operation_id=EXCLUDED.operation_id
      WHERE web_search_call_refs.operation_id=EXCLUDED.operation_id`, call, operation)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return d.Conflict
	}
	return nil
}

// ReadScoped performs fresh product authorization and has no creation path.
func (r *Repository) ReadScoped(ctx context.Context, b d.Binding, scope, id uuid.UUID) (d.Operation, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return d.Operation{}, err
	}
	defer tx.Rollback(ctx)
	if err = authorize(ctx, tx, b.Scope); err != nil {
		return d.Operation{}, err
	}
	if err = binding(ctx, tx, b); err != nil {
		return d.Operation{}, err
	}
	return scan(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.id=COALESCE((SELECT operation_id FROM web_search_call_refs WHERE call_id=$1),$1) AND o.binding_id=$2 AND o.admitted_revision=$3 AND o.search_scope=$4`, id, b.ID, b.Scope.Revision, scope))
}
func (r *Repository) Get(ctx context.Context, b d.Binding, id uuid.UUID) (d.Operation, error) {
	return scan(r.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.id=$1 AND b.id=$2 AND b.tenant_id=$3 AND b.user_id=$4`, id, b.ID, b.Scope.Tenant, b.Scope.User))
}
func (r *Repository) Claim(ctx context.Context, id uuid.UUID, hash string, executor uuid.UUID) (d.Receipt, error) {
	// Scope is recovered from durable admission, never from the executor payload.
	o, err := scan(r.pool.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.id=$1`, id))
	if err != nil {
		return d.Receipt{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return d.Receipt{}, err
	}
	defer tx.Rollback(ctx)
	if o.SearchScope != uuid.Nil && o.Revision != o.Binding.Scope.Revision {
		return d.Receipt{}, d.Denied
	}
	if err = authorize(ctx, tx, o.Binding.Scope); err != nil {
		return d.Receipt{}, err
	}
	if err = binding(ctx, tx, o.Binding); err != nil {
		return d.Receipt{}, err
	}
	o.ClaimID = uuid.New()
	o.Executor = executor
	if executor == uuid.Nil || hash != o.Digest {
		return d.Receipt{}, d.Conflict
	}
	tag, err := tx.Exec(ctx, `UPDATE web_operations SET state='dispatch_committed',dispatch='may_have_dispatched',claim_id=$3,executor_instance=$4,updated_at=now() WHERE id=$1 AND parameter_digest=$2 AND state='ready' AND deadline>now()`, id, hash, o.ClaimID, executor)
	if err != nil {
		return d.Receipt{}, err
	}
	if tag.RowsAffected() != 1 {
		return d.Receipt{}, d.Conflict
	}
	o.State = "dispatch_committed"
	o.Dispatch = "may_have_dispatched"
	if err = event(ctx, tx, o); err != nil {
		return d.Receipt{}, err
	}
	return d.Receipt{Operation: o}, tx.Commit(ctx)
}
func (r *Repository) Finish(ctx context.Context, receipt d.Receipt, state, dispatch string) error {
	if !((state == "completed" && dispatch == "completed") || (state == "unknown" && dispatch == "may_have_dispatched") || (state == "cancelled" && dispatch == "may_have_dispatched")) {
		return d.Invalid
	}
	o := receipt.Operation
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE web_operations SET state=$5,dispatch=$6,transport_fact=COALESCE(NULLIF($8,''),transport_fact),search_outcome=COALESCE(NULLIF($9,''),search_outcome),search_error=NULLIF($10,''),updated_at=now() WHERE id=$1 AND claim_id=$2 AND executor_instance=$3 AND parameter_digest=$4 AND state='dispatch_committed' AND deadline=$7`, o.ID, o.ClaimID, o.Executor, o.Digest, state, dispatch, o.Deadline, o.Facts.Transport, o.Facts.Outcome, string(o.Facts.ErrorCode))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return d.Conflict
	}
	o.State = state
	o.Dispatch = dispatch
	if err = event(ctx, tx, o); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func releaseReady(ctx context.Context, tx pgx.Tx, o *d.Operation, reason d.Code) error {
	changed, err := tx.Exec(ctx, `UPDATE web_operations SET state='cancelled',retryable_before_dispatch=true,
        search_outcome='failed',search_error=$2,updated_at=now()
        WHERE id=$1 AND state='ready' AND dispatch='not_dispatched' AND claim_id IS NULL AND search_scope IS NOT NULL`, o.ID, reason)
	if err != nil {
		return err
	}
	if changed.RowsAffected() != 1 {
		return d.Conflict
	}
	o.State = "cancelled"
	o.Facts.Outcome = "failed"
	o.Facts.ErrorCode = reason
	return event(ctx, tx, *o)
}

func (r *Repository) EndBeforeClaim(ctx context.Context, b d.Binding, id uuid.UUID, reason d.Code) (d.Operation, error) {
	if reason != d.Unavailable && reason != "timeout" {
		return d.Operation{}, d.Invalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return d.Operation{}, err
	}
	defer tx.Rollback(ctx)
	o, err := scan(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.id=$1 AND b.id=$2 AND b.tenant_id=$3 AND b.user_id=$4 FOR UPDATE OF o`, id, b.ID, b.Scope.Tenant, b.Scope.User))
	if err != nil {
		return o, err
	}
	if o.State == "ready" {
		if err = releaseReady(ctx, tx, &o, reason); err != nil {
			return o, err
		}
	}
	return o, tx.Commit(ctx)
}
func (r *Repository) Cancel(ctx context.Context, b d.Binding, id uuid.UUID) (d.Operation, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return d.Operation{}, err
	}
	defer tx.Rollback(ctx)
	o, err := scan(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.id=$1 AND b.id=$2 AND b.tenant_id=$3 AND b.user_id=$4 FOR UPDATE OF o`, id, b.ID, b.Scope.Tenant, b.Scope.User))
	if err != nil {
		return o, err
	}
	if o.SearchScope != uuid.Nil && o.Revision != b.Scope.Revision {
		return o, d.Denied
	}
	if o.State == "ready" || o.State == "dispatch_committed" {
		o.State = "cancelled"
		_, err = tx.Exec(ctx, `UPDATE web_operations SET state='cancelled',updated_at=now() WHERE id=$1`, id)
		if err != nil {
			return o, err
		}
		if err = event(ctx, tx, o); err != nil {
			return o, err
		}
	}
	return o, tx.Commit(ctx)
}
func (r *Repository) Revoke(ctx context.Context, b d.Binding) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE web_bindings SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND tenant_id=$2 AND user_id=$3`, b.ID, b.Scope.Tenant, b.Scope.User)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return d.Denied
	}
	_, err = tx.Exec(ctx, `WITH cancelled AS (UPDATE web_operations SET state='cancelled',updated_at=now() WHERE binding_id=$1 AND state IN ('ready','dispatch_committed') RETURNING id,binding_id,state,dispatch) INSERT INTO web_operation_events(operation_id,binding_id,state,dispatch) SELECT id,binding_id,state,dispatch FROM cancelled`, b.ID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
