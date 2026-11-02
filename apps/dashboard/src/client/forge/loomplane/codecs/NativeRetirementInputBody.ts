import type { Codec } from '../codec-runtime';
import { codec_NativeRetirementInputBody_policy } from './NativeRetirementInputBody.policy';

export const codec_NativeRetirementInputBody: Codec = {"kind": "object", "fields": {"acknowledge_deletion": {"ts": "acknowledgeDeletion"}, "policy": {"ts": "policy", "codec": () => codec_NativeRetirementInputBody_policy}, "request_id": {"ts": "requestId"}}, "required": ["acknowledge_deletion", "policy", "request_id"]};
