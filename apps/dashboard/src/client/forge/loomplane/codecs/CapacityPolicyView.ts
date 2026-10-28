import type { Codec } from '../codec-runtime';
import { codec_CapacityPolicy } from './CapacityPolicy';

export const codec_CapacityPolicyView: Codec = {"kind": "object", "fields": {"automatic_expansion": {"ts": "automaticExpansion"}, "cloud_id": {"ts": "cloudId"}, "policy": {"ts": "policy", "codec": () => codec_CapacityPolicy}}, "required": ["automatic_expansion", "cloud_id"]};
