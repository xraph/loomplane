import { query } from '@forge-go/client-core';
import { op_invitations_list } from '../ops/invitations.list';
import type { OrgInvitationList } from '../types';

export const useInvitationsList = /*#__PURE__*/ query<OrgInvitationList>(op_invitations_list);
