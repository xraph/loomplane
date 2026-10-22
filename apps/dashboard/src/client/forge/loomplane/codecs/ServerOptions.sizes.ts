import type { Codec } from '../codec-runtime';
import { codec_ServerSize } from './ServerSize';

export const codec_ServerOptions_sizes: Codec = {"kind": "array", "items": () => codec_ServerSize};
