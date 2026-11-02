import type { Codec } from '../codec-runtime';
import { codec_NativeRestore } from './NativeRestore';

export const codec_NativeRecoveryCopies_copies: Codec = {"kind": "array", "items": () => codec_NativeRestore};
