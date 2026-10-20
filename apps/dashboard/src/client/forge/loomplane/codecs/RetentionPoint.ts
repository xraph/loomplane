import type { Codec } from '../codec-runtime';

export const codec_RetentionPoint: Codec = {"kind": "object", "fields": {"backup_id": {"ts": "backupId"}, "bytes": {"ts": "bytes"}, "eligible": {"ts": "eligible"}, "reason": {"ts": "reason"}}, "required": ["backup_id", "bytes", "eligible", "reason"]};
