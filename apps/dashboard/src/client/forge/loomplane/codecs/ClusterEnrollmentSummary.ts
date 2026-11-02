import type { Codec } from '../codec-runtime';

export const codec_ClusterEnrollmentSummary: Codec = {"kind": "object", "fields": {"attachment_id": {"ts": "attachmentId"}, "credential_expiry": {"ts": "credentialExpiry"}, "runtime_connection_id": {"ts": "runtimeConnectionId"}, "runtime_verified": {"ts": "runtimeVerified"}, "target_id": {"ts": "targetId"}}, "required": ["attachment_id", "credential_expiry", "runtime_connection_id", "runtime_verified", "target_id"]};
