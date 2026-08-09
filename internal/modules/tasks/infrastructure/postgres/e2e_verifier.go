package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	FEAT126S10Profile = "feat-126-s10-local-lab"
	verifierSchema    = 1
)

// E2EProjection is deliberately limited to control-plane observations. It is
// safe to persist or print because it never reads task content, identifiers,
// request keys, paths, credentials, or database connection material.
type E2EProjection struct {
	SchemaVersion int                `json:"schema_version"`
	Status        string             `json:"status"`
	Profile       string             `json:"profile"`
	RunID         string             `json:"run_id"`
	Tasks         ControlPlaneCounts `json:"tasks"`
	Audit         ControlPlaneCounts `json:"audit"`
	Idempotency   ControlPlaneCounts `json:"idempotency"`
	DenylistHits  int                `json:"denylist_hit_count"`
	CanonicalHash string             `json:"canonical_hash"`
}

type ControlPlaneCounts struct {
	Count         int64    `json:"count"`
	Enums         []string `json:"enums"`
	CanonicalHash string   `json:"canonical_hash"`
}

// VerifyE2E reads only aggregate control-plane fields from the authoritative
// database. The caller must have already validated the explicit local profile
// and migration/schema version.
func VerifyE2E(ctx context.Context, pool *pgxpool.Pool, profile string) (E2EProjection, error) {
	if profile != FEAT126S10Profile || pool == nil {
		return E2EProjection{}, errors.New("e2e verifier profile is invalid")
	}
	tasks, err := aggregate(ctx, pool, `SELECT count(*)::bigint, COALESCE(string_agg(DISTINCT status, ',' ORDER BY status), '') FROM tasks`, "tasks")
	if err != nil {
		return E2EProjection{}, err
	}
	audit, err := aggregate(ctx, pool, `SELECT count(*)::bigint, COALESCE(string_agg(DISTINCT outcome, ',' ORDER BY outcome), '') FROM audit_logs`, "audit")
	if err != nil {
		return E2EProjection{}, err
	}
	idempotency, err := aggregate(ctx, pool, `SELECT count(*)::bigint, COALESCE(string_agg(DISTINCT CASE WHEN task_id IS NULL THEN 'unbound' ELSE 'bound' END, ',' ORDER BY CASE WHEN task_id IS NULL THEN 'unbound' ELSE 'bound' END), '') FROM task_create_idempotency`, "idempotency")
	if err != nil {
		return E2EProjection{}, err
	}
	denylistHits, err := contentDenylistHits(ctx, pool)
	if err != nil {
		return E2EProjection{}, err
	}
	status := "passed"
	if denylistHits != 0 {
		status = "failed"
	}
	projection := E2EProjection{
		SchemaVersion: verifierSchema,
		Status:        status,
		Profile:       profile,
		Tasks:         tasks,
		Audit:         audit,
		Idempotency:   idempotency,
		DenylistHits:  denylistHits,
	}
	canonical, err := json.Marshal(struct {
		Profile     string             `json:"profile"`
		Tasks       ControlPlaneCounts `json:"tasks"`
		Audit       ControlPlaneCounts `json:"audit"`
		Idempotency ControlPlaneCounts `json:"idempotency"`
		Denylist    int                `json:"denylist_hit_count"`
	}{profile, tasks, audit, idempotency, denylistHits})
	if err != nil {
		return E2EProjection{}, fmt.Errorf("canonicalize e2e projection: %w", err)
	}
	digest := sha256.Sum256(canonical)
	projection.CanonicalHash = hex.EncodeToString(digest[:])
	return projection, nil
}

func contentDenylistHits(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	const marker = `(bearer|dsn|private[ _-]?key|password|secret|/users/|\\users\\)`
	var taskHits, auditHits int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::integer
		FROM tasks
		WHERE lower(title) ~ $1
		   OR lower(input::text) ~ $1
		   OR lower(COALESCE(result::text, '')) ~ $1
		   OR lower(COALESCE(error_message, '')) ~ $1
	`, marker).Scan(&taskHits); err != nil {
		return 0, fmt.Errorf("scan task content denylist: %w", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::integer
		FROM audit_logs
		WHERE lower(metadata::text) ~ $1
	`, marker).Scan(&auditHits); err != nil {
		return 0, fmt.Errorf("scan audit content denylist: %w", err)
	}
	return taskHits + auditHits, nil
}

func aggregate(ctx context.Context, pool *pgxpool.Pool, query, name string) (ControlPlaneCounts, error) {
	var count int64
	var encoded string
	if err := pool.QueryRow(ctx, query).Scan(&count, &encoded); err != nil {
		return ControlPlaneCounts{}, fmt.Errorf("aggregate %s control plane: %w", name, err)
	}
	enums := []string{}
	if strings.TrimSpace(encoded) != "" {
		for _, value := range strings.Split(encoded, ",") {
			if value != "" {
				enums = append(enums, value)
			}
		}
	}
	sort.Strings(enums)
	canonical, err := json.Marshal(struct {
		Count int64    `json:"count"`
		Enums []string `json:"enums"`
	}{count, enums})
	if err != nil {
		return ControlPlaneCounts{}, fmt.Errorf("canonicalize %s control plane: %w", name, err)
	}
	digest := sha256.Sum256(canonical)
	return ControlPlaneCounts{Count: count, Enums: enums, CanonicalHash: hex.EncodeToString(digest[:])}, nil
}
