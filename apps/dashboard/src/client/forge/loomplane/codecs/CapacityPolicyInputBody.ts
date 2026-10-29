import type { Codec } from '../codec-runtime';
import { codec_CapacityPolicyInputBody_policy } from './CapacityPolicyInputBody.policy';

export const codec_CapacityPolicyInputBody: Codec = {"kind": "object", "fields": {"expected_revision": {"ts": "expectedRevision"}, "policy": {"ts": "policy", "codec": () => codec_CapacityPolicyInputBody_policy}}, "required": ["expected_revision", "policy"]};
