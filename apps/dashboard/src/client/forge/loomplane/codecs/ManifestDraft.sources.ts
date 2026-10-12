import type { Codec } from '../codec-runtime';
import { codec_SourceRef } from './SourceRef';

export const codec_ManifestDraft_sources: Codec = {"kind": "array", "items": () => codec_SourceRef};
