import type { Codec } from '../codec-runtime';
import { codec_DatabaseLayout_sizes } from './DatabaseLayout.sizes';

export const codec_DatabaseLayout: Codec = {"kind": "object", "fields": {"num_nodes": {"ts": "numNodes"}, "sizes": {"ts": "sizes", "codec": () => codec_DatabaseLayout_sizes}}, "required": ["num_nodes", "sizes"]};
