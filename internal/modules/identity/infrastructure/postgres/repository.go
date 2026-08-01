package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/36Dge/yijie-api/internal/modules/identity/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (repository *Repository) FindByExternalIdentity(ctx context.Context, issuer, subject string) (domain.User, error) {
	if repository == nil || repository.pool == nil {
		return domain.User{}, fmt.Errorf("query external identity: %w", domain.ErrIdentityUnavailable)
	}
	var user domain.User
	err := repository.pool.QueryRow(ctx, `
		SELECT users.id, users.status
		FROM user_identities
		JOIN users ON users.id = user_identities.user_id
		WHERE user_identities.issuer = $1
		  AND user_identities.subject = $2
	`, issuer, subject).Scan(&user.ID, &user.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrIdentityNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("query external identity: %w", err)
	}
	return user, nil
}
