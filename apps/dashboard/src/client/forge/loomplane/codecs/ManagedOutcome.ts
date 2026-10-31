import type { Codec } from '../codec-runtime';

export const codec_ManagedOutcome: Codec = {"kind": "object", "fields": {"Generation": {"ts": "generation"}, "ResourceVersion": {"ts": "resourceVersion"}, "State": {"ts": "state"}, "StatusCode": {"ts": "statusCode"}, "UID": {"ts": "uid"}}, "required": ["Generation", "ResourceVersion", "State", "StatusCode", "UID"]};
