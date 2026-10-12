import type { Codec } from '../codec-runtime';
import { codec_AnalysisList_analyses } from './AnalysisList.analyses';

export const codec_AnalysisList: Codec = {"kind": "object", "fields": {"analyses": {"ts": "analyses", "codec": () => codec_AnalysisList_analyses}}, "required": ["analyses"]};
