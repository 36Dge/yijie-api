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

const operationColumns = `o.id,o.binding_id,b.tenant_id,b.user_id,b.authorization_revision,o.parameter_digest,o.state,o.dispatch,o.deadline,COALESCE(o.claim_id,'00000000-0000-0000-0000-000000000000'),COALESCE(o.executor_instance,'00000000-0000-0000-0000-000000000000')`

func scan(row pgx.Row) (d.Operation, error) {
	var o d.Operation
	err := row.Scan(&o.ID, &o.Binding.ID, &o.Binding.Scope.Tenant, &o.Binding.Scope.User, &o.Binding.Scope.Revision, &o.Digest, &o.State, &o.Dispatch, &o.Deadline, &o.ClaimID, &o.Executor)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, d.Denied
	}
	return o, err
}
func event(ctx context.Context, tx pgx.Tx, o d.Operation) error {
	_, err := tx.Exec(ctx, `INSERT INTO web_operation_events(operation_id,binding_id,state,dispatch) VALUES($1,$2,$3,$4)`, o.ID, o.Binding.ID, o.State, o.Dispatch)
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
	o, err := scan(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM web_operations o JOIN web_bindings b ON b.id=o.binding_id WHERE o.id=$1 AND b.id=$2 AND b.tenant_id=$3 AND b.user_id=$4`, request.ID, b.ID, b.Scope.Tenant, b.Scope.User))
	if err == nil {
		if o.Binding.ID != b.ID || o.Digest != request.Digest {
			return o, false, d.Conflict
		}
		return o, false, nil
	}
	if !errors.Is(err, d.Denied) {
		return o, false, err
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
	o = d.Operation{ID: request.ID, Binding: b, Digest: request.Digest, State: "ready", Dispatch: "not_dispatched", Deadline: now.Add(45 * time.Second).Truncate(time.Millisecond)}
	tag, err := tx.Exec(ctx, `INSERT INTO web_operations(id,binding_id,parameter_digest,tool,state,dispatch,deadline) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING`, o.ID, b.ID, o.Digest, request.Tool, o.State, o.Dispatch, o.Deadline)
	if err != nil {
		return o, false, err
	}
	if tag.RowsAffected() != 1 {
		return o, false, d.Conflict
	}
	if err = event(ctx, tx, o); err != nil {
		return o, false, err
	}
	err = tx.Commit(ctx)
	return o, err == nil, err
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
	tag, err := tx.Exec(ctx, `UPDATE web_operations SET state=$5,dispatch=$6,updated_at=now() WHERE id=$1 AND claim_id=$2 AND executor_instance=$3 AND parameter_digest=$4 AND state='dispatch_committed' AND deadline=$7`, o.ID, o.ClaimID, o.Executor, o.Digest, state, dispatch, o.Deadline)
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
