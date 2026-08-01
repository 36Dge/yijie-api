package domain

import "errors"

var (
	ErrInvalidAuthorizationInput = errors.New("authorization input is invalid")
	ErrUnknownPermission         = errors.New("permission is not in the approved catalog")
	ErrDenied                    = errors.New("authorization is denied")
	ErrAuthorizationUnavailable  = errors.New("authorization repository is unavailable")
)

type Permission string

const (
	KnowledgeRead Permission = "knowledge.read"
	PluginRead    Permission = "plugin.read"
	ScheduleRead  Permission = "schedule.read"
	StoreRead     Permission = "store.read"
	TaskCreate    Permission = "task.create"
	TaskRead      Permission = "task.read"
	WorkspaceUse  Permission = "workspace.use"
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

func (permission Permission) Known() bool {
	for _, candidate := range initialPermissions {
		if permission == candidate {
			return true
		}
	}
	return false
}
