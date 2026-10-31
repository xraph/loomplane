import type { Codec } from '../codec-runtime';
import { codec_ManagedOutcome } from './ManagedOutcome';
import { codec_ManagedRequest } from './ManagedRequest';

export const codec_ManagedCleanupOutcome: Codec = {"kind": "object", "fields": {"epoch": {"ts": "epoch"}, "id": {"ts": "id"}, "outcome": {"ts": "outcome", "codec": () => codec_ManagedOutcome}, "request": {"ts": "request", "codec": () => codec_ManagedRequest}}, "required": ["epoch", "id", "outcome", "request"]};
