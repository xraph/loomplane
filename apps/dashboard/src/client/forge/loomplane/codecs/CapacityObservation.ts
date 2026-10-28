import type { Codec } from '../codec-runtime';

export const codec_CapacityObservation: Codec = {"kind": "object", "fields": {"checked_at": {"ts": "checkedAt"}, "cpu_millis": {"ts": "cpuMillis"}, "free_storage_bytes": {"ts": "freeStorageBytes"}, "memory_bytes": {"ts": "memoryBytes"}, "storage_bytes": {"ts": "storageBytes"}}, "required": ["checked_at"]};
