import type { Codec } from '../codec-runtime';
import { codec_CleanupTarget } from './CleanupTarget';

export const codec_Action_cleanup_targets: Codec = {"kind": "array", "items": () => codec_CleanupTarget};
