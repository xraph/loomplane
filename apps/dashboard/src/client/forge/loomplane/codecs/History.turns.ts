import type { Codec } from '../codec-runtime';
import { codec_Turn } from './Turn';

export const codec_History_turns: Codec = {"kind": "array", "items": () => codec_Turn};
