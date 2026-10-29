import type { Codec } from '../codec-runtime';
import { codec_HeldRoute } from './HeldRoute';

export const codec_Watch_held: Codec = {"kind": "array", "items": () => codec_HeldRoute};
