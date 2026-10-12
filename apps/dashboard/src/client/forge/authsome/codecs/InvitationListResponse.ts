import type { Codec } from '../codec-runtime';
import { codec_InvitationListResponse_invitations } from './InvitationListResponse.invitations';

export const codec_InvitationListResponse: Codec = {"kind": "object", "fields": {"invitations": {"ts": "invitations", "codec": () => codec_InvitationListResponse_invitations}, "next_cursor": {"ts": "nextCursor"}}, "required": ["invitations"]};
