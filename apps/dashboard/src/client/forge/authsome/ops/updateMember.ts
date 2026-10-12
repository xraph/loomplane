import type { OperationMeta } from '../ops';
import { codec_Member } from '../codecs/Member';
import { codec_UpdateMemberRequest } from '../codecs/UpdateMemberRequest';

export const op_updateMember = {
  method: 'PATCH',
  path: '/authsome/v1/orgs/{orgId}/members/{memberId}',
  entity: 'Member',
  rootType: 'Member',
  provides: ['Member:{id}'],
  invalidates: ['Member[]'],
  security: ['bearerAuth'],
  bodyCodec: codec_UpdateMemberRequest,
  responseCodec: codec_Member,
} as const satisfies OperationMeta;
