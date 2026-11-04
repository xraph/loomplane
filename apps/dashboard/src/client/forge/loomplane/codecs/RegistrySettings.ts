import type { Codec } from '../codec-runtime';

export const codec_RegistrySettings: Codec = {"kind": "object", "fields": {"auth_endpoint": {"ts": "authEndpoint"}, "endpoint": {"ts": "endpoint"}, "repository": {"ts": "repository"}}, "required": ["auth_endpoint", "endpoint", "repository"]};
