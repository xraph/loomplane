import type { Codec } from '../codec-runtime';
import { codec_DiscoverySource } from './DiscoverySource';

export const codec_SourceList_sources: Codec = {"kind": "array", "items": () => codec_DiscoverySource};
