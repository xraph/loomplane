import type { Codec } from '../codec-runtime';
import { codec_NewServiceInput } from './NewServiceInput';
import { codec_ServiceSuggestion_env } from './ServiceSuggestion.env';
import { codec_ServiceSuggestion_ports } from './ServiceSuggestion.ports';

export const codec_ServiceSuggestion: Codec = {"kind": "object", "fields": {"ecosystem": {"ts": "ecosystem"}, "env": {"ts": "env", "codec": () => codec_ServiceSuggestion_env}, "framework": {"ts": "framework"}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "path": {"ts": "path"}, "ports": {"ts": "ports", "codec": () => codec_ServiceSuggestion_ports}, "service": {"ts": "service", "codec": () => codec_NewServiceInput}}, "required": ["ecosystem", "id", "kind", "path", "service"]};
