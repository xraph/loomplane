import type { Codec } from '../codec-runtime';

export const codec_Need: Codec = {"kind": "object", "fields": {"gpus": {"ts": "gpus"}, "max_model_len": {"ts": "maxModelLen"}, "per_gpu_gib": {"ts": "perGpuGib"}, "ram_gib": {"ts": "ramGib"}}, "required": ["gpus", "max_model_len", "per_gpu_gib"]};
