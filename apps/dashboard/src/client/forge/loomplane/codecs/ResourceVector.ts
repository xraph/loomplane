import type { Codec } from '../codec-runtime';

export const codec_ResourceVector: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "gpu_kind": {"ts": "gpuKind"}, "gpus": {"ts": "gpus"}, "memory_bytes": {"ts": "memoryBytes"}, "servers": {"ts": "servers"}, "storage_bytes": {"ts": "storageBytes"}}, "required": ["cpu_millis", "memory_bytes", "servers", "storage_bytes"]};
