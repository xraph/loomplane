import type { Codec } from '../codec-runtime';
import { codec_QuantizationView_args } from './QuantizationView.args';
import { codec_QuantizationView_fits } from './QuantizationView.fits';

export const codec_QuantizationView: Codec = {"kind": "object", "fields": {"args": {"ts": "args", "codec": () => codec_QuantizationView_args}, "engine": {"ts": "engine"}, "file": {"ts": "file"}, "fits": {"ts": "fits", "codec": () => codec_QuantizationView_fits}, "id": {"ts": "id"}, "min_gpu_memory_gib": {"ts": "minGpuMemoryGib"}, "min_memory_gib": {"ts": "minMemoryGib"}, "repo": {"ts": "repo"}, "revision": {"ts": "revision"}, "weight_bytes": {"ts": "weightBytes"}}, "required": ["engine", "fits", "id", "weight_bytes"]};
