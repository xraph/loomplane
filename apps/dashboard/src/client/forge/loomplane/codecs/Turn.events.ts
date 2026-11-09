import type { Codec } from '../codec-runtime';
import { codec_Event } from './Event';

export const codec_Turn_events: Codec = {"kind": "array", "items": () => codec_Event};
