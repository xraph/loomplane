import type { Codec } from '../codec-runtime';
import { codec_StartAnalysisInputBody_sources } from './StartAnalysisInputBody.sources';

export const codec_StartAnalysisInputBody: Codec = {"kind": "object", "fields": {"ref": {"ts": "ref"}, "sources": {"ts": "sources", "codec": () => codec_StartAnalysisInputBody_sources}}};
