import type { Codec } from '../codec-runtime';

export const codec_ManagedRegistryBackup: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "id": {"ts": "id"}, "size": {"ts": "size"}}, "required": ["created_at", "id", "size"]};
