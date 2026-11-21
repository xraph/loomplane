import type { Codec } from '../codec-runtime';
import { codec_ProjectCloud } from './ProjectCloud';

export const codec_DefaultCloud: Codec = {"kind": "object", "fields": {"cloud": {"ts": "cloud", "codec": () => codec_ProjectCloud}, "project_id": {"ts": "projectId"}, "revision": {"ts": "revision"}}, "required": ["cloud", "project_id", "revision"]};
