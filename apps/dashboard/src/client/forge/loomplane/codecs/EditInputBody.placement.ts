import type { Codec } from '../codec-runtime';
import { codec_Build } from './Build';
import { codec_Endpoint } from './Endpoint';

export const codec_EditInputBody_placement: Codec = {"kind": "object", "fields": {"build": {"ts": "build", "codec": () => codec_Build}, "enabled": {"ts": "enabled"}, "endpoint": {"ts": "endpoint", "codec": () => codec_Endpoint}, "environment": {"ts": "environment"}, "health_path": {"ts": "healthPath"}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "section": {"ts": "section"}, "target": {"ts": "target"}}, "required": ["name", "section"]};
