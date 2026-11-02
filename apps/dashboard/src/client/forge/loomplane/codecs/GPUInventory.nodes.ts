import type { Codec } from '../codec-runtime';
import { codec_GPUNodeGroup } from './GPUNodeGroup';

export const codec_GPUInventory_nodes: Codec = {"kind": "array", "items": () => codec_GPUNodeGroup};
