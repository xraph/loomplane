import type { Codec } from '../codec-runtime';
import { codec_GPUUsage } from './GPUUsage';

export const codec_CapacityUsage_gpus: Codec = {"kind": "array", "items": () => codec_GPUUsage};
