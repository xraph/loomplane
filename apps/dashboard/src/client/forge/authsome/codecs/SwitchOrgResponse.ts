import type { Codec } from '../codec-runtime';

export const codec_SwitchOrgResponse: Codec = {"kind": "object", "fields": {"expires_at": {"ts": "expiresAt"}, "org_id": {"ts": "orgId"}, "session_id": {"ts": "sessionId"}, "session_token": {"ts": "sessionToken"}}, "required": ["session_id"]};
