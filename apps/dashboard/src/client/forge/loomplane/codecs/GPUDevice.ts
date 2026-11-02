import type { Codec } from '../codec-runtime';

export const codec_GPUDevice: Codec = {"kind": "object", "fields": {"index": {"ts": "index"}, "kind": {"ts": "kind"}, "memory_mib": {"ts": "memoryMib"}, "product": {"ts": "product"}, "uuid": {"ts": "uuid"}}, "required": ["index", "kind", "memory_mib", "product", "uuid"]};
