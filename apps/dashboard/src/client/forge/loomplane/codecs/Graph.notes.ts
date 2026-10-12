import type { Codec } from '../codec-runtime';
import { codec_Note } from './Note';

export const codec_Graph_notes: Codec = {"kind": "array", "items": () => codec_Note};
