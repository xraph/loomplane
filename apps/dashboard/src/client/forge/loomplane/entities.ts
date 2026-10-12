import type { EntityMeta } from './ops';

export const entities = {
  'Analysis': { idField: 'id' },
  'AnalysisGraph': { idField: 'id' },
  'AnalysisList': { fields: { 'analyses': 'Analysis' } },
  'Environment': { idField: 'id' },
  'EnvironmentList': { fields: { 'environments': 'Environment' } },
  'GitHubInstallation': { idField: 'id' },
  'GitHubStatus': { fields: { 'installations': 'GitHubInstallation' } },
  'OrgInvitation': { idField: 'id' },
  'OrgInvitationList': { fields: { 'invitations': 'OrgInvitation' } },
  'OrgMember': { idField: 'id' },
  'OrgMemberList': { fields: { 'members': 'OrgMember' } },
  'Project': { idField: 'id' },
  'ProjectList': { fields: { 'projects': 'Project' } },
} as const satisfies Record<string, EntityMeta>;
