import type { Codec } from '../codec-runtime';

export const codec_Resources: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "memory_mb": {"ts": "memoryMb"}}, "required": ["cpu_millis", "memory_mb"]};
