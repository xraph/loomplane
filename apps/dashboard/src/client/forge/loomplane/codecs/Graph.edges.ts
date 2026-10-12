import type { Codec } from '../codec-runtime';
import { codec_Edge } from './Edge';

export const codec_Graph_edges: Codec = {"kind": "array", "items": () => codec_Edge};
