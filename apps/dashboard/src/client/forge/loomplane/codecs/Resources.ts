import type { Codec } from '../codec-runtime';
import { codec_RecipesGPU } from './RecipesGPU';

export const codec_Resources: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "gpu": {"ts": "gpu", "codec": () => codec_RecipesGPU}, "memory_mb": {"ts": "memoryMb"}, "shm_mb": {"ts": "shmMb"}}, "required": ["cpu_millis", "memory_mb"]};
