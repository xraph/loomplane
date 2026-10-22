import type { Codec } from '../codec-runtime';
import { codec_NativeSource } from './NativeSource';

export const codec_NativeRecoverySources_sources: Codec = {"kind": "array", "items": () => codec_NativeSource};
