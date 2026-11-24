import type { Codec } from '../codec-runtime';
import { codec_OwnUser_environments } from './OwnUser.environments';

export const codec_OwnUser: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_database": {"ts": "createdDatabase"}, "database": {"ts": "database"}, "endpoint_id": {"ts": "endpointId"}, "environments": {"ts": "environments", "codec": () => codec_OwnUser_environments}, "project_id": {"ts": "projectId"}, "revoked_at": {"ts": "revokedAt"}, "service": {"ts": "service"}, "user": {"ts": "user"}}, "required": ["created_at", "created_database", "database", "endpoint_id", "environments", "project_id", "service", "user"]};
