import type { Codec } from '../codec-runtime';

export const codec_ClusterInputBody_retirement: Codec = {"kind": "object", "fields": {"attachment_id": {"ts": "attachmentId"}, "expected_attachment_revision": {"ts": "expectedAttachmentRevision"}, "review_digest": {"ts": "reviewDigest"}}, "required": ["review_digest"]};
