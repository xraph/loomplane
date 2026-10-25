import type { Codec } from '../codec-runtime';

export const codec_NativeCopyCredentials: Codec = {"kind": "object", "fields": {"checked_at": {"ts": "checkedAt"}, "connection_name": {"ts": "connectionName"}, "copy_id": {"ts": "copyId"}, "database": {"ts": "database"}, "expires_at": {"ts": "expiresAt"}, "password": {"ts": "password"}, "private_service_connect": {"ts": "privateServiceConnect"}, "provider": {"ts": "provider"}, "username": {"ts": "username"}}, "required": ["checked_at", "connection_name", "copy_id", "database", "expires_at", "password", "provider", "username"]};
