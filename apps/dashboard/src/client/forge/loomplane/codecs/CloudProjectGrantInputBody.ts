import type { Codec } from '../codec-runtime';
import { codec_CloudProjectGrantInputBody_target_ids } from './CloudProjectGrantInputBody.target_ids';

export const codec_CloudProjectGrantInputBody: Codec = {"kind": "object", "fields": {"target_ids": {"ts": "targetIds", "codec": () => codec_CloudProjectGrantInputBody_target_ids}}, "required": ["target_ids"]};
