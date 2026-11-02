import type { Codec } from '../codec-runtime';
import { codec_WatchedService } from './WatchedService';

export const codec_Watch_services: Codec = {"kind": "array", "items": () => codec_WatchedService};
