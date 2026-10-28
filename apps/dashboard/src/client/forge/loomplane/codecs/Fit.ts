import type { Codec } from '../codec-runtime';
import { codec_Need } from './Need';
import { codec_Result } from './Result';

export const codec_Fit: Codec = {"kind": "object", "fields": {"gpu_kind": {"ts": "gpuKind"}, "need": {"ts": "need", "codec": () => codec_Need}, "reason": {"ts": "reason"}, "result": {"ts": "result", "codec": () => codec_Result}, "status": {"ts": "status"}}, "required": ["gpu_kind", "need", "status"]};
