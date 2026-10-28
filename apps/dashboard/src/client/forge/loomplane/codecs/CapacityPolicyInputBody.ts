import type { Codec } from '../codec-runtime';
import { codec_CapacityPolicy } from './CapacityPolicy';

export const codec_CapacityPolicyInputBody: Codec = {"kind": "object", "fields": {"expected_revision": {"ts": "expectedRevision"}, "policy": {"ts": "policy", "codec": () => codec_CapacityPolicy}}, "required": ["expected_revision", "policy"]};
