import type { Codec } from '../codec-runtime';
import { codec_GPUNodeGroup_node_selector } from './GPUNodeGroup.node_selector';
import { codec_GPUNodeGroup_tolerations } from './GPUNodeGroup.tolerations';

export const codec_GPUNodeGroup: Codec = {"kind": "object", "fields": {"hourly_price_known": {"ts": "hourlyPriceKnown"}, "hourly_usd": {"ts": "hourlyUsd"}, "instance_type": {"ts": "instanceType"}, "kind": {"ts": "kind"}, "memory_gib": {"ts": "memoryGib"}, "node_selector": {"ts": "nodeSelector", "codec": () => codec_GPUNodeGroup_node_selector}, "nodes": {"ts": "nodes"}, "per_node": {"ts": "perNode"}, "product": {"ts": "product"}, "tolerations": {"ts": "tolerations", "codec": () => codec_GPUNodeGroup_tolerations}, "total": {"ts": "total"}}, "required": ["hourly_price_known", "hourly_usd", "kind", "memory_gib", "nodes", "per_node", "total"]};
