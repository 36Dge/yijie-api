// Package domain contains workflow business values, independent of HTTP and storage.
package domain

import "context"

const (
	OwnerID      = "12500000-0000-4000-8000-000000000001"
	TenantID     = "12500000-0000-4000-8000-100000000001"
	ScopeKey     = "feat-153-local-v1"
	MessageBytes = 524288
)

type Failure struct {
	Code        string
	OperationID string
}

func (e *Failure) Error() string { return e.Code }
func Fail(code string) error     { return &Failure{Code: code} }

type Principal struct{ OwnerID, TenantID, UserID, SpaceID string }
type Workflow struct {
	ID, Name, Revision, Canvas, PublishedVersion string
	Runnable                                     bool
	UpdatedAtMS                                  int64
}
type Node struct {
	ID, State, ErrorCode string
	Output               *string
}
type Run struct {
	ID, WorkflowID, OperationID, Mode, Revision, Version, State string
	Terminal                                                    bool
	Input, Output                                               *string
	Nodes                                                       []Node
	StartedAtMS                                                 int64
	FinishedAtMS                                                *int64
	ErrorCode                                                   string
}
type Receipt struct {
	OperationID, Kind, Phase, WorkflowID, Revision, Version, RunID, ErrorCode string
}
type Status struct {
	Ready bool
	Epoch string
}
type Request struct {
	Action, WorkflowID, RunID, OperationID, Name, Revision, Canvas, Input, Version, TestRunID, Cursor string
	Limit                                                                                             int64
}
type Response struct {
	Workflow   *Workflow
	Workflows  []Workflow
	Run        *Run
	Runs       []Run
	Principal  *Principal
	Receipt    *Receipt
	Status     *Status
	NextCursor string
}
type Intent struct{ ID, Kind, WorkflowID, Hash string }

type Engine interface {
	Execute(context.Context, Request) (Response, error)
}
type Repository interface {
	Ping(context.Context) error
	Principal(context.Context) (Principal, error)
	BindPrincipal(context.Context, Principal) error
	BindResources(context.Context, []Workflow) error
	HasResource(context.Context, string) (bool, error)
	Claim(context.Context, Intent) (Receipt, bool, error)
	ReadReceipt(context.Context, string) (Receipt, error)
	Finish(context.Context, Receipt) error
	Audit(context.Context, string, string, string) error
}
