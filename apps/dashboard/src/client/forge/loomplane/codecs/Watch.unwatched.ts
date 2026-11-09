import type { Codec } from '../codec-runtime';
import { codec_UnwatchedService } from './UnwatchedService';

export const codec_Watch_unwatched: Codec = {"kind": "array", "items": () => codec_UnwatchedService};
