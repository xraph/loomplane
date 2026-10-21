import type { Codec } from '../codec-runtime';
import { codec_NativeRecoverySources_sources } from './NativeRecoverySources.sources';

export const codec_NativeRecoverySources: Codec = {"kind": "object", "fields": {"next_before": {"ts": "nextBefore"}, "sources": {"ts": "sources", "codec": () => codec_NativeRecoverySources_sources}}, "required": ["sources"]};
