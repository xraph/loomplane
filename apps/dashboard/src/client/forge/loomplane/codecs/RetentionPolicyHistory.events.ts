import type { Codec } from '../codec-runtime';
import { codec_RetentionPolicyRevision } from './RetentionPolicyRevision';

export const codec_RetentionPolicyHistory_events: Codec = {"kind": "array", "items": () => codec_RetentionPolicyRevision};
