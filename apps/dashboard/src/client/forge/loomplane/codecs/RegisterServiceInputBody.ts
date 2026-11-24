import type { Codec } from '../codec-runtime';
import { codec_RegisterServiceInputBody_projects } from './RegisterServiceInputBody.projects';

export const codec_RegisterServiceInputBody: Codec = {"kind": "object", "fields": {"description": {"ts": "description"}, "dsn": {"ts": "dsn"}, "engine": {"ts": "engine"}, "environment_label": {"ts": "environmentLabel"}, "name": {"ts": "name"}, "projects": {"ts": "projects", "codec": () => codec_RegisterServiceInputBody_projects}, "restricted": {"ts": "restricted"}, "target": {"ts": "target"}}, "required": ["description", "dsn", "engine", "environment_label", "name", "projects", "restricted", "target"]};
