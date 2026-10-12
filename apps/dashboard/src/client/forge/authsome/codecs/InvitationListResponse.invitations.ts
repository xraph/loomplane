import type { Codec } from '../codec-runtime';
import { codec_Invitation } from './Invitation';

export const codec_InvitationListResponse_invitations: Codec = {"kind": "array", "items": () => codec_Invitation};
