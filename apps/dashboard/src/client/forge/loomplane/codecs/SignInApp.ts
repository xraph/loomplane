import type { Codec } from '../codec-runtime';

export const codec_SignInApp: Codec = {"kind": "object", "fields": {"client_id": {"ts": "clientId"}, "cloud": {"ts": "cloud"}, "problem": {"ts": "problem"}, "slug": {"ts": "slug"}, "source": {"ts": "source"}, "updated_at": {"ts": "updatedAt"}, "updated_by": {"ts": "updatedBy"}}, "required": ["cloud", "source"]};
