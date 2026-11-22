import type { Codec } from '../codec-runtime';
import { codec_DatabaseSizes_sizes } from './DatabaseSizes.sizes';

export const codec_DatabaseSizes: Codec = {"kind": "object", "fields": {"sizes": {"ts": "sizes", "codec": () => codec_DatabaseSizes_sizes}}, "required": ["sizes"]};
