-- +goose Up
-- A caller can lose the response to a cross-worker join. Its own opaque handle
-- must remain a read-only route to the canonical operation, even in that case.
CREATE TABLE web_search_call_refs (
    call_id uuid PRIMARY KEY,
    operation_id uuid NOT NULL REFERENCES web_operations(id) ON DELETE CASCADE
);
CREATE INDEX web_search_call_refs_operation ON web_search_call_refs(operation_id);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'search reference migration is expand-only; retain in-flight references';
END $$;
-- +goose StatementEnd
