import type { Codec } from '../codec-runtime';
import { codec_KeylessSetup_steps } from './KeylessSetup.steps';

export const codec_KeylessSetup: Codec = {"kind": "object", "fields": {"audience": {"ts": "audience"}, "connection_id": {"ts": "connectionId"}, "expires_at": {"ts": "expiresAt"}, "issuer": {"ts": "issuer"}, "steps": {"ts": "steps", "codec": () => codec_KeylessSetup_steps}, "subject": {"ts": "subject"}}, "required": ["connection_id", "issuer", "steps", "subject"]};
