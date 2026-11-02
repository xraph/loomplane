import type { Codec } from '../codec-runtime';
import { codec_ClientClusterResourceMetrics_extended } from './ClientClusterResourceMetrics.extended';

export const codec_ClientClusterResourceMetrics: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "ephemeral_storage_bytes": {"ts": "ephemeralStorageBytes"}, "extended": {"ts": "extended", "codec": () => codec_ClientClusterResourceMetrics_extended}, "memory_bytes": {"ts": "memoryBytes"}, "pods": {"ts": "pods"}}, "required": ["extended"]};
