import type { Codec } from '../codec-runtime';
import { codec_ClusterProviderOptions } from './ClusterProviderOptions';
import { codec_NodePoolSpec_labels } from './NodePoolSpec.labels';
import { codec_NodePoolSpec_taints } from './NodePoolSpec.taints';

export const codec_NodePoolSpec: Codec = {"kind": "object", "fields": {"autoscale": {"ts": "autoscale"}, "count": {"ts": "count"}, "id": {"ts": "id"}, "labels": {"ts": "labels", "codec": () => codec_NodePoolSpec_labels}, "max": {"ts": "max"}, "min": {"ts": "min"}, "name": {"ts": "name"}, "operation_id": {"ts": "operationId"}, "provider_options": {"ts": "providerOptions", "codec": () => codec_ClusterProviderOptions}, "request_digest": {"ts": "requestDigest"}, "size": {"ts": "size"}, "taints": {"ts": "taints", "codec": () => codec_NodePoolSpec_taints}}, "required": ["autoscale", "count", "max", "min", "name", "size"]};
