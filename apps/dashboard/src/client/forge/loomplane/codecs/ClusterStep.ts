import type { Codec } from '../codec-runtime';

export const codec_ClusterStep: Codec = {"kind": "object", "fields": {"attempted_at": {"ts": "attemptedAt"}, "completed_at": {"ts": "completedAt"}, "name": {"ts": "name"}, "remote_operation_id": {"ts": "remoteOperationId"}, "remote_resource_id": {"ts": "remoteResourceId"}, "request_digest": {"ts": "requestDigest"}, "state": {"ts": "state"}}, "required": ["name", "request_digest", "state"]};
