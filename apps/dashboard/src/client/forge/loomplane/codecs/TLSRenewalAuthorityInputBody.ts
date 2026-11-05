import type { Codec } from '../codec-runtime';

export const codec_TLSRenewalAuthorityInputBody: Codec = {"kind": "object", "fields": {"active": {"ts": "active"}, "attachment_id": {"ts": "attachmentId"}, "expected_generation": {"ts": "expectedGeneration"}, "id": {"ts": "id"}, "method": {"ts": "method"}, "owner_ref": {"ts": "ownerRef"}, "project_id": {"ts": "projectId"}, "verification_evidence_ref": {"ts": "verificationEvidenceRef"}}, "required": ["active", "attachment_id", "expected_generation", "method", "owner_ref", "project_id", "verification_evidence_ref"]};
