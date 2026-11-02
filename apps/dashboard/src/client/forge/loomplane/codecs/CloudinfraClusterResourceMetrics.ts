import type { Codec } from '../codec-runtime';
import { codec_CloudinfraClusterResourceMetrics_extended } from './CloudinfraClusterResourceMetrics.extended';

export const codec_CloudinfraClusterResourceMetrics: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "ephemeral_storage_bytes": {"ts": "ephemeralStorageBytes"}, "extended": {"ts": "extended", "codec": () => codec_CloudinfraClusterResourceMetrics_extended}, "memory_bytes": {"ts": "memoryBytes"}, "pods": {"ts": "pods"}}, "required": ["extended"]};
