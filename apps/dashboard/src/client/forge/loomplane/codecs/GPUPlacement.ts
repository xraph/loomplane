import type { Codec } from '../codec-runtime';
import { codec_GPUPlacement_node_selector } from './GPUPlacement.node_selector';
import { codec_GPUPlacement_tolerations } from './GPUPlacement.tolerations';

export const codec_GPUPlacement: Codec = {"kind": "object", "fields": {"count": {"ts": "count"}, "kind": {"ts": "kind"}, "node_selector": {"ts": "nodeSelector", "codec": () => codec_GPUPlacement_node_selector}, "tolerations": {"ts": "tolerations", "codec": () => codec_GPUPlacement_tolerations}}, "required": ["count", "kind"]};
