import type { Codec } from '../codec-runtime';
import { codec_Edge_evidence } from './Edge.evidence';

export const codec_Edge: Codec = {"kind": "object", "fields": {"confidence": {"ts": "confidence"}, "evidence": {"ts": "evidence", "codec": () => codec_Edge_evidence}, "from": {"ts": "from"}, "kind": {"ts": "kind"}, "to": {"ts": "to"}}, "required": ["confidence", "evidence", "from", "kind", "to"]};
