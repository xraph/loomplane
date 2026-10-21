import type { Codec } from '../codec-runtime';
import { codec_ZoneInputBody_project_ids } from './ZoneInputBody.project_ids';

export const codec_ZoneInputBody: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "mode": {"ts": "mode"}, "name": {"ts": "name"}, "project_ids": {"ts": "projectIds", "codec": () => codec_ZoneInputBody_project_ids}}, "required": ["mode", "name", "project_ids"]};
