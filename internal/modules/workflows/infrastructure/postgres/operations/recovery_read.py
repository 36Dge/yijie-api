"""Private read-only recovery policy; mirrors this repository's receipt semantics."""
import hashlib

def review(facts, coze_operation_ids):
    if facts.get('schema_version') != 1 or facts.get('database') != 'yijie_workflow_local':
        raise ValueError('API recovery database identity differs')
    bindings = facts['scope_bindings']
    if len(bindings) != 1 or bindings[0]['scope_key'] != 'feat-153-local-v1':
        raise ValueError('API recovery scope differs')
    allowed = []
    for op in facts['operations']:
        if op['scope_key'] != 'feat-153-local-v1' or op['receipt_phase'] != op['phase']:
            raise ValueError('API receipt scope/phase mismatch')
        if op['phase'] in ['completed', 'rejected']:
            continue
        declaration = 'FEAT-153 declared storage qualification; no engine dispatch; ' + op['operation_id']
        audit = [row['outcome_code'] for row in facts['audit'] if row['operation_id'] == op['operation_id']]
        qualification = any(row['workflow_id'] == op['workflow_id'] and row['action'] == 'storage_qualification' and row['outcome_code'] == 'normal_readback' for row in facts['audit'])
        if not (op['phase'] == 'unknown' and op['kind'] == 'save'
                and op['request_sha256'] == hashlib.sha256(declaration.encode()).hexdigest()
                and audit == ['recorded', 'unknown'] and qualification
                and op['operation_id'] not in coze_operation_ids):
            raise ValueError('Unresolved API operation: ' + op['operation_id'])
        allowed.append(op['operation_id'])
    return {'operations': len(facts['operations']), 'resources': len(facts['resources']), 'known_storage_qualification_unknown': allowed}


def review_startup(before, after):
    """EnsurePrincipal in repository.go appends one ordinary principal audit."""
    for key in before:
        if key != 'audit' and before[key] != after[key]:
            raise ValueError('API startup changed stored ' + key)
    old, new = before['audit'], after['audit']
    if new[:len(old)] != old:
        raise ValueError('Original audit history changed')
    added = new[len(old):]
    if len(added) != 1 or any(row['operation_id'] is not None or row['workflow_id'] is not None or row['action'] != 'principal' or row['outcome_code'] != 'success' for row in added):
        raise ValueError('Unexpected startup audit change')
    return {'original_audit_rows_retained': len(old), 'normal_principal_audit_added': added[0]['id']}
