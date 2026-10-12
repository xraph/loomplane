import type { Codec } from '../codec-runtime';

export const codec_CreateOrgRequest: Codec = {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "logo": {"ts": "logo"}, "name": {"ts": "name"}, "slug": {"ts": "slug"}}, "required": ["name", "slug"]};
