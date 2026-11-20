import type { Codec } from '../codec-runtime';
import { codec_ServiceSuggestions_services } from './ServiceSuggestions.services';

export const codec_ServiceSuggestions: Codec = {"kind": "object", "fields": {"analyzed": {"ts": "analyzed"}, "project_id": {"ts": "projectId"}, "services": {"ts": "services", "codec": () => codec_ServiceSuggestions_services}}, "required": ["analyzed", "project_id", "services"]};
