import type { Codec } from '../codec-runtime';
import { codec_Template } from './Template';

export const codec_TemplateRelease: Codec = {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "digest": {"ts": "digest"}, "fleet_id": {"ts": "fleetId"}, "id": {"ts": "id"}, "template": {"ts": "template", "codec": () => codec_Template}}, "required": ["created_at", "digest", "fleet_id", "id", "template"]};
