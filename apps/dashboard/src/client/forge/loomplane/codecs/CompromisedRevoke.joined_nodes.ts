import type { Codec } from '../codec-runtime';
import { codec_Node } from './Node';

export const codec_CompromisedRevoke_joined_nodes: Codec = {"kind": "array", "items": () => codec_Node};
