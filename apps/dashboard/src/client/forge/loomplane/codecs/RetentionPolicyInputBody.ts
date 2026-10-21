import type { Codec } from '../codec-runtime';
import { codec_RetentionPolicyInputBody_policy } from './RetentionPolicyInputBody.policy';

export const codec_RetentionPolicyInputBody: Codec = {"kind": "object", "fields": {"enabled": {"ts": "enabled"}, "policy": {"ts": "policy", "codec": () => codec_RetentionPolicyInputBody_policy}, "revision": {"ts": "revision"}}, "required": ["enabled", "policy", "revision"]};
