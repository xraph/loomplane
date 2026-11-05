import type { Codec } from '../codec-runtime';
import { codec_CloudLocation } from './CloudLocation';

export const codec_CloudLocationList_locations: Codec = {"kind": "array", "items": () => codec_CloudLocation};
