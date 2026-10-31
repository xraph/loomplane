import type { Codec } from '../codec-runtime';
import { codec_ManagedCleanupTarget } from './ManagedCleanupTarget';

export const codec_CleanupReview_targets: Codec = {"kind": "array", "items": () => codec_ManagedCleanupTarget};
