import type { OperationMeta } from '../ops';
import { codec_MemberListResponse } from '../codecs/MemberListResponse';

export const op_listMembers = {
  method: 'GET',
  path: '/authsome/v1/orgs/{orgId}/members',
  entity: 'Member',
  rootType: 'MemberListResponse',
  provides: ['Member:{id}', 'Member[]'],
  invalidates: [],
  security: ['bearerAuth'],
  responseCodec: codec_MemberListResponse,
} as const satisfies OperationMeta;
