import type { Codec } from '../codec-runtime';
import { codec_Graph } from './Graph';

export const codec_AnalysisGraph: Codec = {"kind": "object", "fields": {"graph": {"ts": "graph", "codec": () => codec_Graph}, "id": {"ts": "id"}}, "required": ["graph", "id"]};
