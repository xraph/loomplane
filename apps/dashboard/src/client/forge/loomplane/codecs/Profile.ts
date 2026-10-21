import type { Codec } from '../codec-runtime';
import { codec_DefaultPlacement } from './DefaultPlacement';

export const codec_Profile: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "default_placement": {"ts": "defaultPlacement", "codec": () => codec_DefaultPlacement}, "id": {"ts": "id"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "default_placement", "id", "name", "org_id", "project_id", "revision", "updated_at"]};
