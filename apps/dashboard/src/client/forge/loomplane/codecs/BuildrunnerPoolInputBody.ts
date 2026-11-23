import type { Codec } from '../codec-runtime';
import { codec_BuildrunnerPoolInputBody_settings } from './BuildrunnerPoolInputBody.settings';

export const codec_BuildrunnerPoolInputBody: Codec = {"kind": "object", "fields": {"mode": {"ts": "mode"}, "name": {"ts": "name"}, "project_id": {"ts": "projectId"}, "scope": {"ts": "scope"}, "settings": {"ts": "settings", "codec": () => codec_BuildrunnerPoolInputBody_settings}}, "required": ["name", "scope", "settings"]};
