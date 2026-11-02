import type { Codec } from '../codec-runtime';
import { codec_GPULimit } from './GPULimit';

export const codec_CapacityPolicy_gpu_limits: Codec = {"kind": "array", "items": () => codec_GPULimit};
