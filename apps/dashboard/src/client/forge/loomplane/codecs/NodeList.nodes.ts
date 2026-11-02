import type { Codec } from '../codec-runtime';
import { codec_Node } from './Node';

export const codec_NodeList_nodes: Codec = {"kind": "array", "items": () => codec_Node};
