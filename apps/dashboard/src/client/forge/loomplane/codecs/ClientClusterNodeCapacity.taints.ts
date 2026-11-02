import type { Codec } from '../codec-runtime';
import { codec_NodePoolTaint } from './NodePoolTaint';

export const codec_ClientClusterNodeCapacity_taints: Codec = {"kind": "array", "items": () => codec_NodePoolTaint};
