import type { Codec } from '../codec-runtime';
import { codec_NativeRecoveryCopies_copies } from './NativeRecoveryCopies.copies';

export const codec_NativeRecoveryCopies: Codec = {"kind": "object", "fields": {"copies": {"ts": "copies", "codec": () => codec_NativeRecoveryCopies_copies}, "next_before": {"ts": "nextBefore"}}, "required": ["copies"]};
