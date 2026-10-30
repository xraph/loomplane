import type { Codec } from '../codec-runtime';
import { codec_EdgeInputBody_policy_rules } from './EdgeInputBody.policy.rules';

export const codec_EdgeInputBody_policy: Codec = {"kind": "object", "fields": {"enabled": {"ts": "enabled"}, "invalidate_on_deploy": {"ts": "invalidateOnDeploy"}, "rules": {"ts": "rules", "codec": () => codec_EdgeInputBody_policy_rules}}, "required": ["enabled", "invalidate_on_deploy", "rules"]};
