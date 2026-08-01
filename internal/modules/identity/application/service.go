package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/36Dge/yijie-api/internal/modules/identity/domain"
	"github.com/google/uuid"
)

type UserRepository interface {
	FindByExternalIdentity(ctx context.Context, issuer, subject string) (domain.User, error)
}

type Service struct {
	users UserRepository
}

func New(users UserRepository) *Service {
	return &Service{users: users}
}

func (service *Service) ResolvePrincipal(ctx context.Context, issuer, subject string) (domain.Principal, error) {
	issuer = strings.TrimSpace(issuer)
	subject = strings.TrimSpace(subject)
	if issuer == "" || subject == "" || service == nil || service.users == nil {
		return domain.Principal{}, domain.ErrInvalidIdentity
	}

	user, err := service.users.FindByExternalIdentity(ctx, issuer, subject)
	if errors.Is(err, domain.ErrIdentityNotFound) {
		return domain.Principal{}, domain.ErrIdentityNotFound
	}
	if err != nil {
		return domain.Principal{}, fmt.Errorf("resolve internal identity: %w", domain.ErrIdentityUnavailable)
	}
	if user.ID == uuid.Nil || user.Status != domain.UserActive {
		return domain.Principal{}, domain.ErrUserSuspended
	}
	return domain.Principal{UserID: user.ID, Issuer: issuer, Subject: subject}, nil
}
