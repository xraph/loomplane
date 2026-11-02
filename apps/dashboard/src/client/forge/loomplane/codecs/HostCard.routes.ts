import type { Codec } from '../codec-runtime';
import { codec_BoardRoute } from './BoardRoute';

export const codec_HostCard_routes: Codec = {"kind": "array", "items": () => codec_BoardRoute};
