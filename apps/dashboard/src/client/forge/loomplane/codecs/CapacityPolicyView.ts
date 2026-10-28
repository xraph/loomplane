import type { Codec } from '../codec-runtime';
import { codec_CapacityPolicy } from './CapacityPolicy';

export const codec_CapacityPolicyView: Codec = {"kind": "object", "fields": {"automatic_expansion": {"ts": "automaticExpansion"}, "policy": {"ts": "policy", "codec": () => codec_CapacityPolicy}}, "required": ["automatic_expansion"]};
