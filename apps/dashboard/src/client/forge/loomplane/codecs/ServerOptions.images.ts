import type { Codec } from '../codec-runtime';
import { codec_ServerImage } from './ServerImage';

export const codec_ServerOptions_images: Codec = {"kind": "array", "items": () => codec_ServerImage};
