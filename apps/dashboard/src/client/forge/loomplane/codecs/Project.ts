import type { Codec } from '../codec-runtime';
import { codec_Project_exclusions } from './Project.exclusions';
import { codec_Project_repositories } from './Project.repositories';

export const codec_Project: Codec = {"kind": "object", "fields": {"color": {"ts": "color"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "description": {"ts": "description"}, "exclusions": {"ts": "exclusions", "codec": () => codec_Project_exclusions}, "icon": {"ts": "icon"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "repositories": {"ts": "repositories", "codec": () => codec_Project_repositories}, "slug": {"ts": "slug"}, "status": {"ts": "status"}, "updated_at": {"ts": "updatedAt"}}, "required": ["color", "created_at", "created_by", "description", "exclusions", "icon", "id", "name", "org_id", "repositories", "slug", "status", "updated_at"]};
