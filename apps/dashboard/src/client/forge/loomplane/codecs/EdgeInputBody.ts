import type { Codec } from '../codec-runtime';
import { codec_EdgeInputBody_policy } from './EdgeInputBody.policy';

export const codec_EdgeInputBody: Codec = {"kind": "object", "fields": {"policy": {"ts": "policy", "codec": () => codec_EdgeInputBody_policy}, "revision": {"ts": "revision"}}, "required": ["policy", "revision"]};
