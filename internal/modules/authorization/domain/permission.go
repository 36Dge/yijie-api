package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidAuthorizationInput = errors.New("authorization input is invalid")
	ErrUnknownPermission         = errors.New("permission is not in the approved catalog")
	ErrDenied                    = errors.New("authorization is denied")
	ErrAuthorizationUnavailable  = errors.New("authorization repository is unavailable")
	ErrInvalidProjection         = errors.New("authorization projection is invalid")
)

type Projection struct {
	TenantID              uuid.UUID
	AuthorizationRevision int64
	ExpiresAt             time.Time
	Permissions           []Permission
}

type Permission string

const (
	KnowledgeRead Permission = "knowledge.read"
	PluginRead    Permission = "plugin.read"
	ScheduleRead  Permission = "schedule.read"
	StoreRead     Permission = "store.read"
	TaskCreate    Permission = "task.create"
	TaskRead      Permission = "task.read"
	WorkspaceUse  Permission = "workspace.use"
	// WebRetrieve is exposed by builtin-web-retrieval/1, not the v1 projection.
	WebRetrieve Permission = "web.retrieve"
)

var initialPermissions = [...]Permission{
	KnowledgeRead,
	PluginRead,
	ScheduleRead,
	StoreRead,
	TaskCreate,
	TaskRead,
	WorkspaceUse,
}

func InitialPermissions() []Permission {
	result := make([]Permission, len(initialPermissions))
	copy(result, initialPermissions[:])
	return result
}

// DefaultProductPermissions is used when provisioning a new default role.
// Legacy v1 capability projection keeps its original seven-value wire enum.
func DefaultProductPermissions() []Permission { return append(InitialPermissions(), WebRetrieve) }

func (permission Permission) Known() bool {
	if permission == WebRetrieve {
		return true
	}
	for _, candidate := range initialPermissions {
		if permission == candidate {
			return true
		}
	}
	return false
}
