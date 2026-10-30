import type { Codec } from '../codec-runtime';
import { codec_EdgeRule } from './EdgeRule';

export const codec_EdgeInputBody_policy_rules: Codec = {"kind": "array", "items": () => codec_EdgeRule};
