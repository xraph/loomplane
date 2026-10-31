import type { Codec } from '../codec-runtime';
import { codec_ManagedCleanupBindingRef } from './ManagedCleanupBindingRef';

export const codec_ManagedCleanupTarget_binding_refs: Codec = {"kind": "array", "items": () => codec_ManagedCleanupBindingRef};
