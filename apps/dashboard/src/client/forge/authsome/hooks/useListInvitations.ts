import { query } from '@forge-go/client-core';
import { op_listInvitations } from '../ops/listInvitations';
import type { InvitationListResponse } from '../types';

export const useListInvitations = /*#__PURE__*/ query<InvitationListResponse>(op_listInvitations);
