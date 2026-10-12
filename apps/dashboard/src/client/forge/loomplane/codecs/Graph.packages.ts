import type { Codec } from '../codec-runtime';
import { codec_Package } from './Package';

export const codec_Graph_packages: Codec = {"kind": "array", "items": () => codec_Package};
