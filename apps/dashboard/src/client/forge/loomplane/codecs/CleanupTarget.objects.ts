import type { Codec } from '../codec-runtime';
import { codec_CleanupObject } from './CleanupObject';

export const codec_CleanupTarget_objects: Codec = {"kind": "array", "items": () => codec_CleanupObject};
