import type { Codec } from '../codec-runtime';
import { codec_EdgePolicy_rules } from './EdgePolicy.rules';

export const codec_EdgePolicy: Codec = {"kind": "object", "fields": {"enabled": {"ts": "enabled"}, "invalidate_on_deploy": {"ts": "invalidateOnDeploy"}, "rules": {"ts": "rules", "codec": () => codec_EdgePolicy_rules}}, "required": ["enabled", "invalidate_on_deploy", "rules"]};
