import type { Codec } from '../codec-runtime';
import { codec_ScalingPolicy } from './ScalingPolicy';

export const codec_ActionInputBody_scale: Codec = {"kind": "object", "fields": {"expected_policy_revision": {"ts": "expectedPolicyRevision"}, "policy": {"ts": "policy", "codec": () => codec_ScalingPolicy}, "service": {"ts": "service"}}, "required": ["expected_policy_revision", "policy", "service"]};
