import type { Codec } from '../codec-runtime';
import { codec_OrgInvitationList_invitations } from './OrgInvitationList.invitations';

export const codec_OrgInvitationList: Codec = {"kind": "object", "fields": {"invitations": {"ts": "invitations", "codec": () => codec_OrgInvitationList_invitations}}, "required": ["invitations"]};
