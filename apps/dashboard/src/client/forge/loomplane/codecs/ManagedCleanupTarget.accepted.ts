import type { Codec } from '../codec-runtime';
import { codec_ManagedCleanupOutcome } from './ManagedCleanupOutcome';

export const codec_ManagedCleanupTarget_accepted: Codec = {"kind": "array", "items": () => codec_ManagedCleanupOutcome};
