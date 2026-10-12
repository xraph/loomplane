import type { EntityMeta } from './ops';

export const entities = {
  'Invitation': { idField: 'id' },
  'InvitationListResponse': { fields: { 'invitations': 'Invitation' } },
  'Member': { idField: 'id' },
  'MemberListResponse': { fields: { 'members': 'Member' } },
  'OrgListResponse': { fields: { 'organizations': 'Organization' } },
  'Organization': { idField: 'id' },
} as const satisfies Record<string, EntityMeta>;
