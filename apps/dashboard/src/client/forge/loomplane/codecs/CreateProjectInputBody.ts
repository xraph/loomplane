import type { Codec } from '../codec-runtime';

export const codec_CreateProjectInputBody: Codec = {"kind": "object", "fields": {"color": {"ts": "color"}, "description": {"ts": "description"}, "icon": {"ts": "icon"}, "name": {"ts": "name"}, "repository_url": {"ts": "repositoryUrl"}, "slug": {"ts": "slug"}}, "required": ["name"]};
