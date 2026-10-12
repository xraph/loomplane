import type { Codec } from '../codec-runtime';
import { codec_Route } from './Route';

export const codec_ServiceConfig_gateway: Codec = {"kind": "array", "items": () => codec_Route};
