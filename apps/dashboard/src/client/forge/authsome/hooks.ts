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

import type { Invitation, InvitationListResponse, Member, MemberListResponse, OrgListResponse, Organization, SlugAvailableResponse } from './types';

export const useSwitchOrg = mutation(ops.switchOrg);
export const useListOrganizations = query<OrgListResponse>(ops.listOrganizations);
export const useCreateOrganization = mutation<Organization, Organization>(ops.createOrganization);
export const useCheckOrgSlug = query<SlugAvailableResponse>(ops.checkOrgSlug);
export const useAcceptInvitation = mutation<Member, Member>(ops.acceptInvitation);
export const useDeclineInvitation = mutation(ops.declineInvitation);
export const useGetOrganization = query<Organization>(ops.getOrganization);
export const useUpdateOrganization = mutation<Organization, Organization>(ops.updateOrganization);
export const useDeleteOrganization = mutation(ops.deleteOrganization);
export const useListInvitations = query<InvitationListResponse>(ops.listInvitations);
export const useCreateInvitation = mutation<Invitation, Invitation>(ops.createInvitation);
export const useListMembers = query<MemberListResponse>(ops.listMembers);
export const useAddMember = mutation<Member, Member>(ops.addMember);
export const useUpdateMember = mutation<Member, Member>(ops.updateMember);
export const useRemoveMember = mutation(ops.removeMember);
