import type { Codec } from '../codec-runtime';

export const codec_PublicConfig: Codec = {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "auth_base_path": {"ts": "authBasePath"}, "publishable_key": {"ts": "publishableKey"}}, "required": ["app_id", "auth_base_path", "publishable_key"]};
