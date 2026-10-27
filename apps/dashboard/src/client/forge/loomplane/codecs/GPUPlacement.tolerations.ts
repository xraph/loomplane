import type { Codec } from '../codec-runtime';
import { codec_GPUToleration } from './GPUToleration';

export const codec_GPUPlacement_tolerations: Codec = {"kind": "array", "items": () => codec_GPUToleration};
