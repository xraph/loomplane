import type { Codec } from '../codec-runtime';
import { codec_OrgInvitation } from './OrgInvitation';

export const codec_OrgInvitationList_invitations: Codec = {"kind": "array", "items": () => codec_OrgInvitation};
