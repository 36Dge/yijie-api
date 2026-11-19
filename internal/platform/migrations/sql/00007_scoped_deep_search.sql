-- +goose Up
ALTER TABLE web_operations
    ADD COLUMN search_scope uuid,
    ADD COLUMN admitted_revision bigint,
    ADD COLUMN transport_fact text NOT NULL DEFAULT 'uncertain'
        CHECK (transport_fact IN ('uncertain','attempted','responded')),
    ADD COLUMN search_outcome text NOT NULL DEFAULT 'pending'
        CHECK (search_outcome IN ('pending','results','empty','partial','failed','unknown')),
    ADD COLUMN search_error text,
    ADD COLUMN retryable_before_dispatch boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT retryable_only_before_dispatch CHECK (NOT retryable_before_dispatch OR
        (state='cancelled' AND dispatch='not_dispatched' AND claim_id IS NULL AND search_scope IS NOT NULL)),
    ADD CONSTRAINT scoped_search_identity CHECK (
        (search_scope IS NULL AND admitted_revision IS NULL) OR
        (search_scope IS NOT NULL AND admitted_revision IS NOT NULL AND admitted_revision > 0 AND tool='kimi_web_search_pro'));
CREATE UNIQUE INDEX web_operations_scoped_search ON web_operations
    (binding_id,admitted_revision,search_scope,parameter_digest) WHERE search_scope IS NOT NULL AND NOT retryable_before_dispatch;
ALTER TABLE web_operation_events ADD COLUMN transport_fact text, ADD COLUMN search_outcome text;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'scoped search migration is expand-only; retain operation history';
END $$;
-- +goose StatementEnd
