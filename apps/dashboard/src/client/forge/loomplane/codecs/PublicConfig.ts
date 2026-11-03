import type { Codec } from '../codec-runtime';
import { codec_Features } from './Features';

export const codec_PublicConfig: Codec = {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "auth_base_path": {"ts": "authBasePath"}, "console_path": {"ts": "consolePath"}, "features": {"ts": "features", "codec": () => codec_Features}, "publishable_key": {"ts": "publishableKey"}, "require_email_verification": {"ts": "requireEmailVerification"}, "setup_pending": {"ts": "setupPending"}, "setup_token_required": {"ts": "setupTokenRequired"}, "waitlist": {"ts": "waitlist"}}, "required": ["app_id", "auth_base_path", "features", "publishable_key", "require_email_verification", "setup_pending", "setup_token_required", "waitlist"]};
