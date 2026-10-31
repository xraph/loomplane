import type { Codec } from '../codec-runtime';

export const codec_ClusterWorkloadVector: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "ephemeral_storage_bytes": {"ts": "ephemeralStorageBytes"}, "memory_bytes": {"ts": "memoryBytes"}, "pods": {"ts": "pods"}}, "required": ["cpu_millis", "ephemeral_storage_bytes", "memory_bytes", "pods"]};
