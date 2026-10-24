import type { Codec } from '../codec-runtime';

export const codec_NativePITRBounds: Codec = {"kind": "object", "fields": {"earliest": {"ts": "earliest"}, "latest": {"ts": "latest"}, "protocol": {"ts": "protocol"}}, "required": ["earliest", "latest", "protocol"]};
