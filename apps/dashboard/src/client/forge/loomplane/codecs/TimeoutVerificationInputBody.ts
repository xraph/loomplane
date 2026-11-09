import type { Codec } from '../codec-runtime';
import { codec_TimeoutVerificationInputBody_drain_policy } from './TimeoutVerificationInputBody.drain_policy';

export const codec_TimeoutVerificationInputBody: Codec = {"kind": "object", "fields": {"attachment_id": {"ts": "attachmentId"}, "config_revision": {"ts": "configRevision"}, "drain_policy": {"ts": "drainPolicy", "codec": () => codec_TimeoutVerificationInputBody_drain_policy}, "evidence_ref": {"ts": "evidenceRef"}, "expected_attachment_revision": {"ts": "expectedAttachmentRevision"}}, "required": ["attachment_id", "config_revision", "drain_policy", "evidence_ref", "expected_attachment_revision"]};
