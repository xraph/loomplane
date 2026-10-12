/**
 * Typed hooks over the operation manifest.
 *
 * Generated. Each line is a binding, not an implementation.
 *
 * The bodies live in @forge-go/client-core, so a runtime defect is fixed by
 * upgrading that package rather than by regenerating this one.
 */

import { query, mutation } from '@forge-go/client-core';
import { ops } from './ops';

import type { EnvironmentList, InfrastructureStatus, OrgInvitationList, OrgMemberList, Project, ProjectList, PublicConfig, Session } from './types';

export const useInfrastructureGet = query<InfrastructureStatus>(ops['infrastructure.get']);
export const useInvitationsList = query<OrgInvitationList>(ops['invitations.list']);
export const useInvitationsCreate = mutation(ops['invitations.create']);
export const useMembersList = query<OrgMemberList>(ops['members.list']);
export const useProjectsList = query<ProjectList>(ops['projects.list']);
export const useProjectsCreate = mutation<Project, Project>(ops['projects.create']);
export const useProjectsGet = query<Project>(ops['projects.get']);
export const useProjectsUpdate = mutation<Project, Project>(ops['projects.update']);
export const useProjectsDelete = mutation(ops['projects.delete']);
export const useEnvironmentsList = query<EnvironmentList>(ops['environments.list']);
export const usePublicConfigGet = query<PublicConfig>(ops['publicConfig.get']);
export const useSessionGet = query<Session>(ops['session.get']);
