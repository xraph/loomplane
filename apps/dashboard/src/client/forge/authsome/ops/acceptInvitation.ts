import type { OperationMeta } from '../ops';
import { codec_AcceptInvitationRequest } from '../codecs/AcceptInvitationRequest';
import { codec_Member } from '../codecs/Member';

export const op_acceptInvitation = {
  method: 'POST',
  path: '/authsome/v1/orgs/invitations/accept',
  entity: 'Member',
  rootType: 'Member',
  provides: ['Member:{id}'],
  invalidates: ['Member[]'],
  security: ['bearerAuth'],
  bodyCodec: codec_AcceptInvitationRequest,
  responseCodec: codec_Member,
} as const satisfies OperationMeta;
