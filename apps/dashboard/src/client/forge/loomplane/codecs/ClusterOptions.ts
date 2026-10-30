import type { Codec } from '../codec-runtime';
import { codec_ClusterOptions_networks } from './ClusterOptions.networks';
import { codec_ClusterOptions_node_size_facts } from './ClusterOptions.node_size_facts';
import { codec_ClusterOptions_node_sizes } from './ClusterOptions.node_sizes';
import { codec_ClusterOptions_regions } from './ClusterOptions.regions';
import { codec_ClusterOptions_versions } from './ClusterOptions.versions';
import { codec_ClusterProviderOptions } from './ClusterProviderOptions';

export const codec_ClusterOptions: Codec = {"kind": "object", "fields": {"account_id": {"ts": "accountId"}, "checked_at": {"ts": "checkedAt"}, "connection_generation": {"ts": "connectionGeneration"}, "digest": {"ts": "digest"}, "networks": {"ts": "networks", "codec": () => codec_ClusterOptions_networks}, "node_size_facts": {"ts": "nodeSizeFacts", "codec": () => codec_ClusterOptions_node_size_facts}, "node_sizes": {"ts": "nodeSizes", "codec": () => codec_ClusterOptions_node_sizes}, "provider_options": {"ts": "providerOptions", "codec": () => codec_ClusterProviderOptions}, "regions": {"ts": "regions", "codec": () => codec_ClusterOptions_regions}, "versions": {"ts": "versions", "codec": () => codec_ClusterOptions_versions}}, "required": ["account_id", "checked_at", "connection_generation", "digest", "networks", "node_size_facts", "node_sizes", "regions", "versions"]};
