import type { Codec } from '../codec-runtime';
import { codec_SourceState } from './SourceState';

export const codec_ProjectView_sources: Codec = {"kind": "array", "items": () => codec_SourceState};
