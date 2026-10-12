import type { OperationMeta } from '../ops';
import { codec_CreateInvitationRequest } from '../codecs/CreateInvitationRequest';
import { codec_Invitation } from '../codecs/Invitation';

export const op_createInvitation = {
  method: 'POST',
  path: '/authsome/v1/orgs/{orgId}/invitations',
  entity: 'Invitation',
  rootType: 'Invitation',
  provides: ['Invitation:{id}'],
  invalidates: ['Invitation[]'],
  security: ['bearerAuth'],
  bodyCodec: codec_CreateInvitationRequest,
  responseCodec: codec_Invitation,
} as const satisfies OperationMeta;
