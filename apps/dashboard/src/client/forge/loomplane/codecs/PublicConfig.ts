import type { Codec } from '../codec-runtime';
import { codec_Features } from './Features';

export const codec_PublicConfig: Codec = {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "auth_base_path": {"ts": "authBasePath"}, "features": {"ts": "features", "codec": () => codec_Features}, "publishable_key": {"ts": "publishableKey"}}, "required": ["app_id", "auth_base_path", "features", "publishable_key"]};
