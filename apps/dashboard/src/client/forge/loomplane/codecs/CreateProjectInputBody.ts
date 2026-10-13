import type { Codec } from '../codec-runtime';
import { codec_CreateProjectInputBody_repositories } from './CreateProjectInputBody.repositories';

export const codec_CreateProjectInputBody: Codec = {"kind": "object", "fields": {"color": {"ts": "color"}, "description": {"ts": "description"}, "icon": {"ts": "icon"}, "name": {"ts": "name"}, "repositories": {"ts": "repositories", "codec": () => codec_CreateProjectInputBody_repositories}, "repository_url": {"ts": "repositoryUrl"}, "slug": {"ts": "slug"}}, "required": ["name"]};
