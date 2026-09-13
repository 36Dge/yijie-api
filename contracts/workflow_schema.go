package contracts

import _ "embed"

// WorkflowSchema is copied by the canonical Contracts consumer sync, never edited here.
//
//go:embed workflow-local.schema.json
var WorkflowSchema []byte

// WorkflowSourceLock is the exact canonical producer lock copied by consumer sync.
//
//go:embed workflow-local.source-lock.json
var WorkflowSourceLock []byte
