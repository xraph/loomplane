import type { Codec } from '../codec-runtime';
import { codec_Usage_environments } from './Usage.environments';

export const codec_Usage: Codec = {"kind": "object", "fields": {"cloud_id": {"ts": "cloudId"}, "environments": {"ts": "environments", "codec": () => codec_Usage_environments}}, "required": ["cloud_id", "environments"]};
