import type { Codec } from '../codec-runtime';

export const codec_Capabilities: Codec = {"kind": "object", "fields": {"create_database": {"ts": "createDatabase"}, "create_user": {"ts": "createUser"}}, "required": ["create_database", "create_user"]};
