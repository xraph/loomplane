import type { Codec } from '../codec-runtime';
import { codec_RetirementInputBody_policy } from './RetirementInputBody.policy';

export const codec_RetirementInputBody: Codec = {"kind": "object", "fields": {"policy": {"ts": "policy", "codec": () => codec_RetirementInputBody_policy}, "request_id": {"ts": "requestId"}}, "required": ["policy", "request_id"]};
