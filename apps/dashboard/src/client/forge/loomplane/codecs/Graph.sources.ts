import type { Codec } from '../codec-runtime';
import { codec_SourceRef } from './SourceRef';

export const codec_Graph_sources: Codec = {"kind": "array", "items": () => codec_SourceRef};
