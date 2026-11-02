import type { Codec } from '../codec-runtime';

export const codec_CatalogconnectEndpoint: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "database": {"ts": "database"}, "engine": {"ts": "engine"}, "host": {"ts": "host"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "port": {"ts": "port"}, "revision": {"ts": "revision"}, "tls": {"ts": "tls"}, "username": {"ts": "username"}}, "required": ["created_at", "created_by", "engine", "host", "id", "name", "org_id", "port", "revision", "tls"]};
