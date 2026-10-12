import type { OperationMeta } from '../ops';
import { codec_DeclineInvitationRequest } from '../codecs/DeclineInvitationRequest';
import { codec_OrganizationStatusResponse } from '../codecs/OrganizationStatusResponse';

export const op_declineInvitation = {
  method: 'POST',
  path: '/authsome/v1/orgs/invitations/decline',
  provides: [],
  invalidates: [],
  security: ['bearerAuth'],
  bodyCodec: codec_DeclineInvitationRequest,
  responseCodec: codec_OrganizationStatusResponse,
} as const satisfies OperationMeta;
