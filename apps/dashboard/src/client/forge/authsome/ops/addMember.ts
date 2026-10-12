import type { OperationMeta } from '../ops';
import { codec_AddMemberRequest } from '../codecs/AddMemberRequest';
import { codec_Member } from '../codecs/Member';

export const op_addMember = {
  method: 'POST',
  path: '/authsome/v1/orgs/{orgId}/members',
  entity: 'Member',
  rootType: 'Member',
  provides: ['Member:{id}'],
  invalidates: ['Member[]'],
  security: ['bearerAuth'],
  bodyCodec: codec_AddMemberRequest,
  responseCodec: codec_Member,
} as const satisfies OperationMeta;
