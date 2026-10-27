import type { Codec } from '../codec-runtime';
import { codec_ResourceStep } from './ResourceStep';

export const codec_Resources: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "memory_mb": {"ts": "memoryMb"}, "per_gpu": {"ts": "perGpu", "codec": () => codec_ResourceStep}}, "required": ["cpu_millis", "memory_mb"]};
