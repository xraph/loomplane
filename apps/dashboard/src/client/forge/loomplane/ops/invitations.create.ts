import type { OperationMeta } from '../ops';
import { codec_CreateInvitationInputBody } from '../codecs/CreateInvitationInputBody';
import { codec_CreatedInvitation } from '../codecs/CreatedInvitation';

export const op_invitations_create = {
  method: 'POST',
  path: '/api/v1/invitations',
  provides: [],
  invalidates: ['OrgInvitation[]'],
  bodyCodec: codec_CreateInvitationInputBody,
  responseCodec: codec_CreatedInvitation,
} as const satisfies OperationMeta;
