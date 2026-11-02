import type { Codec } from '../codec-runtime';

export const codec_RetainedResource: Codec = {"kind": "object", "fields": {"estimated_hourly_micro_usd": {"ts": "estimatedHourlyMicroUsd"}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "ownership": {"ts": "ownership"}, "survives_cluster_deletion": {"ts": "survivesClusterDeletion"}}, "required": ["id", "kind", "ownership", "survives_cluster_deletion"]};
