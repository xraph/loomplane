import type { Codec } from '../codec-runtime';
import { codec_EnvUse_evidence } from './EnvUse.evidence';

export const codec_EnvUse: Codec = {"kind": "object", "fields": {"confidence": {"ts": "confidence"}, "evidence": {"ts": "evidence", "codec": () => codec_EnvUse_evidence}, "example": {"ts": "example"}, "name": {"ts": "name"}}, "required": ["confidence", "evidence", "name"]};
