import type { Codec } from '../codec-runtime';

export const codec_SetupRequest: Codec = {"kind": "object", "fields": {"email": {"ts": "email"}, "name": {"ts": "name"}, "organization_name": {"ts": "organizationName"}, "password": {"ts": "password"}, "token": {"ts": "token"}}, "required": ["email", "name", "organization_name", "password"]};
