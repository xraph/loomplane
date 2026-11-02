import type { Codec } from '../codec-runtime';

export const codec_ChallengeCredentialInputBody: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "revision": {"ts": "revision"}}, "required": ["connection_id", "revision"]};
