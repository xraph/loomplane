import type { Codec } from '../codec-runtime';
import { codec_ClusterMaintenance } from './ClusterMaintenance';
import { codec_ClusterProviderOptions } from './ClusterProviderOptions';
import { codec_ClusterSpec_node_pools } from './ClusterSpec.node_pools';

export const codec_ClusterSpec: Codec = {"kind": "object", "fields": {"ha": {"ts": "ha"}, "maintenance": {"ts": "maintenance", "codec": () => codec_ClusterMaintenance}, "name": {"ts": "name"}, "network_id": {"ts": "networkId"}, "node_pools": {"ts": "nodePools", "codec": () => codec_ClusterSpec_node_pools}, "operation_id": {"ts": "operationId"}, "provider_options": {"ts": "providerOptions", "codec": () => codec_ClusterProviderOptions}, "region": {"ts": "region"}, "version": {"ts": "version"}}, "required": ["ha", "maintenance", "name", "network_id", "node_pools", "operation_id", "region", "version"]};
