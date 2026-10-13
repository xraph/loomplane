import type { Codec } from '../codec-runtime';

export const codec_Collection: Codec = {"kind": "object", "fields": {"action_id": {"ts": "actionId"}, "attempt": {"ts": "attempt"}, "deployment_id": {"ts": "deploymentId"}, "id": {"ts": "id"}, "message": {"ts": "message"}, "operation_id": {"ts": "operationId"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "state": {"ts": "state"}, "updated_at": {"ts": "updatedAt"}}, "required": ["action_id", "attempt", "deployment_id", "id", "message", "operation_id", "org_id", "project_id", "state", "updated_at"]};
