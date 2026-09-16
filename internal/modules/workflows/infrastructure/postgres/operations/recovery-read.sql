-- FEAT-154 recovery: private read-only operational report, no API or data writes.
-- Scope/receipt meanings follow repository.go and the fixed workflow migrations.
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT json_build_object(
  'schema_version', 1, 'database', current_database(),
  'scope_bindings', (SELECT coalesce(json_agg(row_to_json(x)), '[]') FROM (SELECT scope_key, coze_user_id, coze_space_id FROM workflow_scope_bindings ORDER BY scope_key) x),
  'resources', (SELECT coalesce(json_agg(workflow_id ORDER BY workflow_id), '[]') FROM workflow_resources WHERE scope_key='feat-153-local-v1'),
  'operations', (SELECT coalesce(json_agg(row_to_json(x)), '[]') FROM (SELECT operation_id, scope_key, kind, request_sha256, workflow_id, phase, receipt->>'Phase' AS receipt_phase, receipt->>'ErrorCode' AS error_code FROM workflow_operations ORDER BY operation_id) x),
  'audit', (SELECT coalesce(json_agg(row_to_json(x)), '[]') FROM (SELECT id, operation_id, workflow_id, action, outcome_code FROM workflow_audit WHERE scope_key='feat-153-local-v1' ORDER BY id) x)
);
ROLLBACK;
