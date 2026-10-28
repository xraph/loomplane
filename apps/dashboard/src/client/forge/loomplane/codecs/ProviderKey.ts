import type { Codec } from '../codec-runtime';
import { codec_ProviderKey_fields } from './ProviderKey.fields';
import { codec_ProviderKey_models } from './ProviderKey.models';
import { codec_ProviderKey_projects } from './ProviderKey.projects';

export const codec_ProviderKey: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "fields": {"ts": "fields", "codec": () => codec_ProviderKey_fields}, "id": {"ts": "id"}, "key_hint": {"ts": "keyHint"}, "models": {"ts": "models", "codec": () => codec_ProviderKey_models}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "projects": {"ts": "projects", "codec": () => codec_ProviderKey_projects}, "revision": {"ts": "revision"}, "type": {"ts": "type"}, "updated_at": {"ts": "updatedAt"}, "updated_by": {"ts": "updatedBy"}}, "required": ["created_at", "created_by", "id", "name", "org_id", "projects", "revision", "type", "updated_at", "updated_by"]};
