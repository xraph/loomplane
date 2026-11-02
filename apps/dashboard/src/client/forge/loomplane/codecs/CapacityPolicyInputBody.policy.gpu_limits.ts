import type { Codec } from '../codec-runtime';
import { codec_GPULimit } from './GPULimit';

export const codec_CapacityPolicyInputBody_policy_gpu_limits: Codec = {"kind": "array", "items": () => codec_GPULimit};
