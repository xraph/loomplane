import type { Codec } from '../codec-runtime';

export const codec_RuntimeHealth: Codec = {"kind": "object", "fields": {"architecture": {"ts": "architecture"}, "checked_at": {"ts": "checkedAt"}, "cpus": {"ts": "cpus"}, "docker_id": {"ts": "dockerId"}, "effective_cpu_millis": {"ts": "effectiveCpuMillis"}, "effective_memory_bytes": {"ts": "effectiveMemoryBytes"}, "memory_bytes": {"ts": "memoryBytes"}}, "required": ["architecture", "checked_at", "cpus", "docker_id", "effective_cpu_millis", "effective_memory_bytes", "memory_bytes"]};
