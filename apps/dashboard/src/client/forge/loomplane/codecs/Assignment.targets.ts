import type { Codec } from '../codec-runtime';
import { codec_Target } from './Target';

export const codec_Assignment_targets: Codec = {"kind": "array", "items": () => codec_Target};
