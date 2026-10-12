import type { OperationMeta } from '../ops';
import { codec_OrgInvitationList } from '../codecs/OrgInvitationList';

export const op_invitations_list = {
  method: 'GET',
  path: '/api/v1/invitations',
  entity: 'OrgInvitation',
  rootType: 'OrgInvitationList',
  provides: ['OrgInvitation:{id}', 'OrgInvitation[]'],
  invalidates: [],
  responseCodec: codec_OrgInvitationList,
} as const satisfies OperationMeta;
