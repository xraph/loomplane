import type { Codec } from '../codec-runtime';
import { codec_SetupStep } from './SetupStep';

export const codec_KeylessSetup_steps: Codec = {"kind": "array", "items": () => codec_SetupStep};
