import type { Codec } from '../codec-runtime';

export const codec_GPUUsage: Codec = {"kind": "object", "fields": {"kind": {"ts": "kind"}, "limit": {"ts": "limit"}, "pending": {"ts": "pending"}, "provisioned": {"ts": "provisioned"}, "reserved": {"ts": "reserved"}}, "required": ["kind", "limit", "pending", "provisioned", "reserved"]};
