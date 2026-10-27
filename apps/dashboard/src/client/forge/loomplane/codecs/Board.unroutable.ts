import type { Codec } from '../codec-runtime';
import { codec_UnroutableService } from './UnroutableService';

export const codec_Board_unroutable: Codec = {"kind": "array", "items": () => codec_UnroutableService};
