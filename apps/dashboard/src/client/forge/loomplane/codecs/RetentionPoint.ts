import type { Codec } from '../codec-runtime';

export const codec_RetentionPoint: Codec = {"kind": "object", "fields": {"backup_id": {"ts": "backupId"}, "bytes": {"ts": "bytes"}, "created_at": {"ts": "createdAt"}, "deletion_supported": {"ts": "deletionSupported"}, "deletion_unavailable_reason": {"ts": "deletionUnavailableReason"}, "eligible": {"ts": "eligible"}, "reason": {"ts": "reason"}}, "required": ["backup_id", "bytes", "created_at", "deletion_supported", "eligible", "reason"]};
