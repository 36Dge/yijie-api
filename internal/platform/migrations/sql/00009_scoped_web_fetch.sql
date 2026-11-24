-- +goose Up
-- The existing scoped unique index includes the tool in parameter_digest.
ALTER TABLE web_operations DROP CONSTRAINT scoped_search_identity,
    ADD CONSTRAINT scoped_search_identity CHECK (
        (search_scope IS NULL AND admitted_revision IS NULL) OR
        (search_scope IS NOT NULL AND admitted_revision > 0 AND admitted_revision IS NOT NULL
         AND tool IN ('kimi_web_search_pro','kimi_web_fetch')));

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'scoped fetch is expand-only; retain operation history';
END $$;
-- +goose StatementEnd
