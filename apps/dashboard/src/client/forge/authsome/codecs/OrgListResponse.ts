import type { Codec } from '../codec-runtime';
import { codec_OrgListResponse_organizations } from './OrgListResponse.organizations';

export const codec_OrgListResponse: Codec = {"kind": "object", "fields": {"next_cursor": {"ts": "nextCursor"}, "organizations": {"ts": "organizations", "codec": () => codec_OrgListResponse_organizations}}, "required": ["organizations"]};
