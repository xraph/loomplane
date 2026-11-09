import type { Codec } from '../codec-runtime';
import { codec_NodeList_nodes } from './NodeList.nodes';

export const codec_NodeList: Codec = {"kind": "object", "fields": {"nodes": {"ts": "nodes", "codec": () => codec_NodeList_nodes}}, "required": ["nodes"]};
