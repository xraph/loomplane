import type { EntityMeta } from './ops';

export const entities = {
  'Environment': { idField: 'id' },
  'EnvironmentList': { fields: { 'environments': 'Environment' } },
  'OrgInvitation': { idField: 'id' },
  'OrgInvitationList': { fields: { 'invitations': 'OrgInvitation' } },
  'OrgMember': { idField: 'id' },
  'OrgMemberList': { fields: { 'members': 'OrgMember' } },
  'Project': { idField: 'id' },
  'ProjectList': { fields: { 'projects': 'Project' } },
} as const satisfies Record<string, EntityMeta>;
