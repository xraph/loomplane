import type { Codec } from '../codec-runtime';
import { codec_Resource_evidence } from './Resource.evidence';

export const codec_Resource: Codec = {"kind": "object", "fields": {"confidence": {"ts": "confidence"}, "evidence": {"ts": "evidence", "codec": () => codec_Resource_evidence}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "needs_confirmation": {"ts": "needsConfirmation"}}, "required": ["confidence", "evidence", "id", "kind", "needs_confirmation"]};
