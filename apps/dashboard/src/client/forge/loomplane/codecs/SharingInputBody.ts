import type { Codec } from '../codec-runtime';
import { codec_SharingInputBody_projects } from './SharingInputBody.projects';

export const codec_SharingInputBody: Codec = {"kind": "object", "fields": {"projects": {"ts": "projects", "codec": () => codec_SharingInputBody_projects}, "restricted": {"ts": "restricted"}}, "required": ["projects", "restricted"]};
