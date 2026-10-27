import type { Codec } from '../codec-runtime';
import { codec_ServicePort } from './ServicePort';

export const codec_Board_unrouted: Codec = {"kind": "array", "items": () => codec_ServicePort};
