import type { Codec } from '../codec-runtime';
import { codec_KeylessStep } from './KeylessStep';

export const codec_KeylessSetup_steps: Codec = {"kind": "array", "items": () => codec_KeylessStep};
