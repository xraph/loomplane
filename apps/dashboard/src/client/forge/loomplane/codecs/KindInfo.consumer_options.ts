import type { Codec } from '../codec-runtime';
import { codec_ConsumerOption } from './ConsumerOption';

export const codec_KindInfo_consumer_options: Codec = {"kind": "array", "items": () => codec_ConsumerOption};
