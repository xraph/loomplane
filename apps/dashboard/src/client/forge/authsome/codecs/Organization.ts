import type { Codec } from '../codec-runtime';
import { codec_Organization_metadata } from './Organization.metadata';

export const codec_Organization: Codec = {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "env_id": {"ts": "envId"}, "id": {"ts": "id"}, "logo": {"ts": "logo"}, "metadata": {"ts": "metadata", "codec": () => codec_Organization_metadata}, "name": {"ts": "name"}, "slug": {"ts": "slug"}, "updated_at": {"ts": "updatedAt"}}, "required": ["app_id", "created_at", "created_by", "env_id", "id", "name", "slug", "updated_at"]};
