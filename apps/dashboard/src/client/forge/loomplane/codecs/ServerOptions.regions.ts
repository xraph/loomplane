import type { Codec } from '../codec-runtime';
import { codec_ServerRegion } from './ServerRegion';

export const codec_ServerOptions_regions: Codec = {"kind": "array", "items": () => codec_ServerRegion};
