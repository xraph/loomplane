import type { Codec } from '../codec-runtime';
import { codec_Findings_suggestions } from './Findings.suggestions';

export const codec_Findings: Codec = {"kind": "object", "fields": {"confidence": {"ts": "confidence"}, "likely_cause": {"ts": "likelyCause"}, "suggestions": {"ts": "suggestions", "codec": () => codec_Findings_suggestions}, "summary": {"ts": "summary"}, "written_at": {"ts": "writtenAt"}}, "required": ["confidence", "likely_cause", "suggestions", "summary", "written_at"]};
