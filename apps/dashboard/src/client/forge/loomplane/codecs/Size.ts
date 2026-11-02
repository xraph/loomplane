import type { Codec } from '../codec-runtime';
import { codec_ManifestGPU } from './ManifestGPU';

export const codec_Size: Codec = {"kind": "object", "fields": {"cpu": {"ts": "cpu"}, "gpu": {"ts": "gpu", "codec": () => codec_ManifestGPU}, "memory": {"ts": "memory"}, "shm": {"ts": "shm"}}, "required": ["cpu", "memory"]};
