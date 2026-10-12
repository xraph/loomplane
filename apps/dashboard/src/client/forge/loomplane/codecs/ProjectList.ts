import type { Codec } from '../codec-runtime';
import { codec_ProjectList_projects } from './ProjectList.projects';

export const codec_ProjectList: Codec = {"kind": "object", "fields": {"projects": {"ts": "projects", "codec": () => codec_ProjectList_projects}}, "required": ["projects"]};
