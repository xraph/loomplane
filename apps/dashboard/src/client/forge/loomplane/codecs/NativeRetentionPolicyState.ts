import type { Codec } from '../codec-runtime';
import { codec_NativeRetentionPolicy } from './NativeRetentionPolicy';

export const codec_NativeRetentionPolicyState: Codec = {"kind": "object", "fields": {"configured": {"ts": "configured"}, "policy": {"ts": "policy", "codec": () => codec_NativeRetentionPolicy}}, "required": ["configured"]};
