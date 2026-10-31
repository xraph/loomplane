import type { Codec } from '../codec-runtime';
import { codec_ManagedCleanupTarget } from './ManagedCleanupTarget';

export const codec_Action_cleanup_targets: Codec = {"kind": "array", "items": () => codec_ManagedCleanupTarget};
