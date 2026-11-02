import type { Codec } from '../codec-runtime';
import { codec_RetentionInput } from './RetentionInput';

export const codec_NativeRetirement: Codec = {"kind": "object", "fields": {"completed_at": {"ts": "completedAt"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "error": {"ts": "error"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "point_id": {"ts": "pointId"}, "policy": {"ts": "policy", "codec": () => codec_RetentionInput}, "policy_id": {"ts": "policyId"}, "policy_revision": {"ts": "policyRevision"}, "project_id": {"ts": "projectId"}, "source_id": {"ts": "sourceId"}, "status": {"ts": "status"}, "witness_id": {"ts": "witnessId"}}, "required": ["created_at", "created_by", "id", "org_id", "point_id", "policy", "project_id", "source_id", "status", "witness_id"]};
