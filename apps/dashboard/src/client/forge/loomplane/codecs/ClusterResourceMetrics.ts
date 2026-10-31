import type { Codec } from '../codec-runtime';
import { codec_ClusterResourceMetrics_extended } from './ClusterResourceMetrics.extended';

export const codec_ClusterResourceMetrics: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "ephemeral_storage_bytes": {"ts": "ephemeralStorageBytes"}, "extended": {"ts": "extended", "codec": () => codec_ClusterResourceMetrics_extended}, "memory_bytes": {"ts": "memoryBytes"}, "pods": {"ts": "pods"}}, "required": ["extended"]};
