import type { OperationMeta } from '../ops';
import { codec_InvitationListResponse } from '../codecs/InvitationListResponse';

export const op_listInvitations = {
  method: 'GET',
  path: '/authsome/v1/orgs/{orgId}/invitations',
  entity: 'Invitation',
  rootType: 'InvitationListResponse',
  provides: ['Invitation:{id}', 'Invitation[]'],
  invalidates: [],
  security: ['bearerAuth'],
  responseCodec: codec_InvitationListResponse,
} as const satisfies OperationMeta;
