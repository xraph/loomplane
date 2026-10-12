import type { Codec } from '../codec-runtime';
import { codec_Runs } from './Runs';

export const codec_Provider_runs: Codec = {"kind": "array", "items": () => codec_Runs};
