import type { Codec } from '../codec-runtime';

export const codec_NodeWorkload: Codec = {"kind": "object", "fields": {"binding_id": {"ts": "bindingId"}, "environment": {"ts": "environment"}, "has_backup_policy": {"ts": "hasBackupPolicy"}, "kind": {"ts": "kind"}, "project_id": {"ts": "projectId"}, "service": {"ts": "service"}}, "required": ["binding_id", "environment", "has_backup_policy", "kind", "project_id", "service"]};
