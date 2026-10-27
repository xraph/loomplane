import type { Codec } from '../codec-runtime';

export const codec_SyncResult: Codec = {"kind": "object", "fields": {"generation": {"ts": "generation"}, "message": {"ts": "message"}, "restarted": {"ts": "restarted"}, "restored": {"ts": "restored"}, "state": {"ts": "state"}}, "required": ["generation", "restarted", "restored", "state"]};
