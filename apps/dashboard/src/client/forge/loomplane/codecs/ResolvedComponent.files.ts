import type { Codec } from '../codec-runtime';
import { codec_File } from './File';

export const codec_ResolvedComponent_files: Codec = {"kind": "array", "items": () => codec_File};
