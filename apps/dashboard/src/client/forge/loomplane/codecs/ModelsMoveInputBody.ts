import type { Codec } from '../codec-runtime';
import { codec_ModelsMoveInputBody_server } from './ModelsMoveInputBody.server';
import { codec_ModelsMoveInputBody_target } from './ModelsMoveInputBody.target';

export const codec_ModelsMoveInputBody: Codec = {"kind": "object", "fields": {"max_model_len": {"ts": "maxModelLen"}, "quantization": {"ts": "quantization"}, "review_server": {"ts": "reviewServer"}, "server": {"ts": "server", "codec": () => codec_ModelsMoveInputBody_server}, "target": {"ts": "target", "codec": () => codec_ModelsMoveInputBody_target}}, "required": ["target"]};
