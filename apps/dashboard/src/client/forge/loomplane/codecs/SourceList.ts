import type { Codec } from '../codec-runtime';
import { codec_SourceList_sources } from './SourceList.sources';

export const codec_SourceList: Codec = {"kind": "object", "fields": {"sources": {"ts": "sources", "codec": () => codec_SourceList_sources}}, "required": ["sources"]};
