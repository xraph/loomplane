import type { Codec } from '../codec-runtime';
import { codec_Finding } from './Finding';

export const codec_Plan_warnings: Codec = {"kind": "array", "items": () => codec_Finding};
