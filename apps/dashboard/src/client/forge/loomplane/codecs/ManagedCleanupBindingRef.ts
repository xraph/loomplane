import type { Codec } from '../codec-runtime';
import { codec_ManagedCleanupBinding } from './ManagedCleanupBinding';

export const codec_ManagedCleanupBindingRef: Codec = {"kind": "object", "fields": {"binding": {"ts": "binding", "codec": () => codec_ManagedCleanupBinding}, "origin": {"ts": "origin"}, "parent_binding_id": {"ts": "parentBindingId"}}, "required": ["binding", "origin", "parent_binding_id"]};
