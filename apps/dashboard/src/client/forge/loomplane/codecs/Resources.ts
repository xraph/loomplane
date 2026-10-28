import type { Codec } from '../codec-runtime';
import { codec_RecipesGPU } from './RecipesGPU';
import { codec_ResourceStep } from './ResourceStep';

export const codec_Resources: Codec = {"kind": "object", "fields": {"cpu_millis": {"ts": "cpuMillis"}, "gpu": {"ts": "gpu", "codec": () => codec_RecipesGPU}, "memory_mb": {"ts": "memoryMb"}, "per_gpu": {"ts": "perGpu", "codec": () => codec_ResourceStep}, "shm_mb": {"ts": "shmMb"}}, "required": ["cpu_millis", "memory_mb"]};
