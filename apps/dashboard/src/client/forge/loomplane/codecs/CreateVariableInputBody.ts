import type { Codec } from '../codec-runtime';

export const codec_CreateVariableInputBody: Codec = {"kind": "object", "fields": {"environment": {"ts": "environment"}, "key": {"ts": "key"}, "secret": {"ts": "secret"}, "service": {"ts": "service"}, "value": {"ts": "value"}}, "required": ["key", "value"]};
