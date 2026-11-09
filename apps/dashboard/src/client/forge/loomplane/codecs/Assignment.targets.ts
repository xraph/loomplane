import type { Codec } from '../codec-runtime';
import { codec_CloudprofilesTarget } from './CloudprofilesTarget';

export const codec_Assignment_targets: Codec = {"kind": "array", "items": () => codec_CloudprofilesTarget};
