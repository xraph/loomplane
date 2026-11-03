import type { Codec } from '../codec-runtime';

export const codec_JoinToken: Codec = {"kind": "object", "fields": {"command": {"ts": "command"}, "expires_at": {"ts": "expiresAt"}, "token": {"ts": "token"}}, "required": ["command", "expires_at", "token"]};
