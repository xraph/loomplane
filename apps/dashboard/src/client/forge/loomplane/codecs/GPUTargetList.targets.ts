import type { Codec } from '../codec-runtime';
import { codec_GPUTarget } from './GPUTarget';

export const codec_GPUTargetList_targets: Codec = {"kind": "array", "items": () => codec_GPUTarget};
