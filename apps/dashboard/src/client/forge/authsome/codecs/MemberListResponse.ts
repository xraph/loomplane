import type { Codec } from '../codec-runtime';
import { codec_MemberListResponse_members } from './MemberListResponse.members';

export const codec_MemberListResponse: Codec = {"kind": "object", "fields": {"members": {"ts": "members", "codec": () => codec_MemberListResponse_members}, "next_cursor": {"ts": "nextCursor"}}, "required": ["members"]};
