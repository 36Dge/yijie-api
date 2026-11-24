// Package domain contains the server-owned retrieval admission state.
package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type Code string

func (c Code) Error() string { return string(c) }

const (
	Invalid      Code = "invalid_arguments"
	Expired      Code = "session_expired"
	Denied       Code = "not_entitled"
	Quota        Code = "quota_exceeded"
	Busy         Code = "busy"
	Conflict     Code = "operation_conflict"
	OldOperation Code = "operation_expired"
	Unavailable  Code = "upstream_unavailable"
)

func ErrorCode(err error) Code {
	var code Code
	if errors.As(err, &code) {
		return code
	}
	return Unavailable
}

type AdmissionFailure struct{ Cause error }

func (e AdmissionFailure) Error() string { return "web admission rejected" }
func (e AdmissionFailure) Unwrap() error { return e.Cause }

type Scope struct {
	User, Tenant uuid.UUID
	Revision     int64
}
type Binding struct {
	ID    uuid.UUID
	Scope Scope
}
type Grant struct {
	Binding            Binding
	ID                 uuid.UUID
	Bearer             string
	CredentialRevision int64
	Expires            time.Time
}

func (Grant) String() string { return "[web delegation redacted]" }

type SearchFacts struct {
	Transport string
	Outcome   string
	ErrorCode Code
}
type Operation struct {
	Tool              string
	SearchScope       uuid.UUID
	Revision          int64
	Facts             SearchFacts
	ID                uuid.UUID
	Binding           Binding
	Digest            string
	State, Dispatch   string
	Deadline          time.Time
	ClaimID, Executor uuid.UUID
}
type Request struct {
	SearchScope uuid.UUID
	Deadline    time.Time
	ID          uuid.UUID
	Tool        string
	Arguments   []byte
	Digest      string
}
type Receipt struct{ Operation Operation }
type Result struct {
	Existing  bool
	Operation Operation
	Body      []byte
	Failure   Code
}

// Repository owns durable admission. Replaying an ID never admits again.
type Repository interface {
	Mint(context.Context, Scope, uuid.UUID) (Grant, error)
	Resolve(context.Context, string) (Binding, error)
	FindBinding(context.Context, Scope, uuid.UUID) (Binding, error)
	Revoke(context.Context, Binding) error
	Admit(context.Context, Binding, Request) (Operation, bool, error)
	Get(context.Context, Binding, uuid.UUID) (Operation, error)
	ReadScoped(context.Context, Binding, uuid.UUID, uuid.UUID) (Operation, error)
	GetInternal(context.Context, uuid.UUID) (Operation, error)
	Cancel(context.Context, Binding, uuid.UUID) (Operation, error)
	EndBeforeClaim(context.Context, Binding, uuid.UUID, Code) (Operation, error)
	Claim(context.Context, uuid.UUID, string, uuid.UUID) (Receipt, error)
	Finish(context.Context, Receipt, string, string) error
}
type Executor interface {
	Execute(context.Context, Operation, Request) ([]byte, error)
}
