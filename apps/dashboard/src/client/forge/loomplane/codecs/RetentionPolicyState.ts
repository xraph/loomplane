import type { Codec } from '../codec-runtime';
import { codec_RetentionPolicy } from './RetentionPolicy';

export const codec_RetentionPolicyState: Codec = {"kind": "object", "fields": {"configured": {"ts": "configured"}, "policy": {"ts": "policy", "codec": () => codec_RetentionPolicy}}, "required": ["configured"]};
