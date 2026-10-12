import type { Codec } from '../codec-runtime';
import { codec_Service } from './Service';

export const codec_Graph_services: Codec = {"kind": "array", "items": () => codec_Service};
