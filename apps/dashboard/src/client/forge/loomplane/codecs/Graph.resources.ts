import type { Codec } from '../codec-runtime';
import { codec_Resource } from './Resource';

export const codec_Graph_resources: Codec = {"kind": "array", "items": () => codec_Resource};
