import type { OperationMeta } from '../ops';
import { codec_OrgMemberList } from '../codecs/OrgMemberList';

export const op_members_list = {
  method: 'GET',
  path: '/api/v1/members',
  entity: 'OrgMember',
  rootType: 'OrgMemberList',
  provides: ['OrgMember:{id}', 'OrgMember[]'],
  invalidates: [],
  responseCodec: codec_OrgMemberList,
} as const satisfies OperationMeta;
