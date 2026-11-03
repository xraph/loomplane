import type { Codec } from '../codec-runtime';
import { codec_NodeprotoGPU } from './NodeprotoGPU';

export const codec_NodeJoinBody_inventory_gpus: Codec = {"kind": "array", "items": () => codec_NodeprotoGPU};
