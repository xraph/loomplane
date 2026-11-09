import type { Codec } from '../codec-runtime';
import { codec_LocationVerificationInputBody_failure_domains } from './LocationVerificationInputBody.failure_domains';

export const codec_LocationVerificationInputBody: Codec = {"kind": "object", "fields": {"cloud_revision": {"ts": "cloudRevision"}, "evidence_ref": {"ts": "evidenceRef"}, "failure_domains": {"ts": "failureDomains", "codec": () => codec_LocationVerificationInputBody_failure_domains}, "revision": {"ts": "revision"}}, "required": ["cloud_revision", "evidence_ref", "failure_domains", "revision"]};
