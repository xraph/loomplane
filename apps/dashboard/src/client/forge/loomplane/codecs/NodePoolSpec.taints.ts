import type { Codec } from '../codec-runtime';
import { codec_NodePoolTaint } from './NodePoolTaint';

export const codec_NodePoolSpec_taints: Codec = {"kind": "array", "items": () => codec_NodePoolTaint};
