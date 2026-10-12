import { query } from '@forge-go/client-core';
import { op_members_list } from '../ops/members.list';
import type { OrgMemberList } from '../types';

export const useMembersList = /*#__PURE__*/ query<OrgMemberList>(op_members_list);
