import type { Codec } from '../codec-runtime';

export const codec_SignInApp: Codec = {"kind": "object", "fields": {"client_id": {"ts": "clientId"}, "cloud": {"ts": "cloud"}, "source": {"ts": "source"}, "updated_at": {"ts": "updatedAt"}, "updated_by": {"ts": "updatedBy"}}, "required": ["cloud", "source"]};
