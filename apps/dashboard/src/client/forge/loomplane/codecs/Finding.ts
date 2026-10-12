import type { Codec } from '../codec-runtime';
import { codec_Finding_evidence } from './Finding.evidence';

export const codec_Finding: Codec = {"kind": "object", "fields": {"code": {"ts": "code"}, "evidence": {"ts": "evidence", "codec": () => codec_Finding_evidence}, "message": {"ts": "message"}, "name": {"ts": "name"}, "path": {"ts": "path"}, "subject": {"ts": "subject"}}, "required": ["code", "message"]};
