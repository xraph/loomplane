import { query } from '@forge-go/client-core';
import { op_listMembers } from '../ops/listMembers';
import type { MemberListResponse } from '../types';

export const useListMembers = /*#__PURE__*/ query<MemberListResponse>(op_listMembers);
