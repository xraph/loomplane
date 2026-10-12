import type { RequestConfig } from './fetch';
import { Client } from './client';
import * as types from './types';
import { CODECS } from './codecs';

export class RESTClient extends Client {
  /**
   * Accept invitation
   * Accepts a pending organization invitation using the invitation token.
   */
  public readonly acceptInvitation = async (body: types.AcceptInvitationRequest, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Member> => {
    let __path = `/authsome/v1/orgs/invitations/accept`;
    const config: RequestConfig = {
      method: 'POST',
      url: __path,
      body,
      bodyCodec: CODECS["AcceptInvitationRequest"],
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["Member"],
    };

    return this.request<types.Member>(config);
  };

  /**
   * Add member
   * Adds a user as a member of an organization.
   */
  public readonly addMember = async (orgId: string, body: types.AddMemberRequest, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Member> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}/members`;
    const config: RequestConfig = {
      method: 'POST',
      url: __path,
      body,
      bodyCodec: CODECS["AddMemberRequest"],
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["Member"],
    };

    return this.request<types.Member>(config);
  };

  /**
   * Check slug availability
   * Checks whether an organization slug is available.
   */
  public readonly checkOrgSlug = async (appId?: string, slug?: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SlugAvailableResponse> => {
    let __path = `/authsome/v1/orgs/check-slug`;
    const queryParams: Record<string, any> = {};
    queryParams['app_id'] = appId;
    queryParams['slug'] = slug;
    const queryString = new URLSearchParams(queryParams).toString();
    if (queryString) {
      __path += '?' + queryString;
    }
    const config: RequestConfig = {
      method: 'GET',
      url: __path,
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["SlugAvailableResponse"],
    };

    return this.request<types.SlugAvailableResponse>(config);
  };

  /**
   * Create invitation
   * Creates an invitation to join an organization.
   */
  public readonly createInvitation = async (orgId: string, body: types.CreateInvitationRequest, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Invitation> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}/invitations`;
    const config: RequestConfig = {
      method: 'POST',
      url: __path,
      body,
      bodyCodec: CODECS["CreateInvitationRequest"],
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["Invitation"],
    };

    return this.request<types.Invitation>(config);
  };

  /**
   * Create organization
   * Creates a new organization. The authenticated user becomes the owner.
   */
  public readonly createOrganization = async (body: types.CreateOrgRequest, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Organization> => {
    let __path = `/authsome/v1/orgs`;
    const config: RequestConfig = {
      method: 'POST',
      url: __path,
      body,
      bodyCodec: CODECS["CreateOrgRequest"],
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["Organization"],
    };

    return this.request<types.Organization>(config);
  };

  /**
   * Decline invitation
   * Declines a pending organization invitation using the invitation token.
   */
  public readonly declineInvitation = async (body: types.DeclineInvitationRequest, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OrganizationStatusResponse> => {
    let __path = `/authsome/v1/orgs/invitations/decline`;
    const config: RequestConfig = {
      method: 'POST',
      url: __path,
      body,
      bodyCodec: CODECS["DeclineInvitationRequest"],
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["OrganizationStatusResponse"],
    };

    return this.request<types.OrganizationStatusResponse>(config);
  };

  /**
   * Delete organization
   * Deletes an organization and all its members.
   */
  public readonly deleteOrganization = async (orgId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OrganizationStatusResponse> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}`;
    const config: RequestConfig = {
      method: 'DELETE',
      url: __path,
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["OrganizationStatusResponse"],
    };

    return this.request<types.OrganizationStatusResponse>(config);
  };

  /**
   * Get organization
   * Returns details of a specific organization.
   */
  public readonly getOrganization = async (orgId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Organization> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}`;
    const config: RequestConfig = {
      method: 'GET',
      url: __path,
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["Organization"],
    };

    return this.request<types.Organization>(config);
  };

  /**
   * List invitations
   * Returns all pending invitations for an organization.
   */
  public readonly listInvitations = async (orgId: string, limit?: number | undefined, cursor?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.InvitationListResponse> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}/invitations`;
    const queryParams: Record<string, any> = {};
    if (limit !== undefined) {
      queryParams['limit'] = limit;
    }
    if (cursor !== undefined) {
      queryParams['cursor'] = cursor;
    }
    const queryString = new URLSearchParams(queryParams).toString();
    if (queryString) {
      __path += '?' + queryString;
    }
    const config: RequestConfig = {
      method: 'GET',
      url: __path,
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["InvitationListResponse"],
    };

    return this.request<types.InvitationListResponse>(config);
  };

  /**
   * List members
   * Returns all members of an organization.
   */
  public readonly listMembers = async (orgId: string, limit?: number | undefined, cursor?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.MemberListResponse> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}/members`;
    const queryParams: Record<string, any> = {};
    if (limit !== undefined) {
      queryParams['limit'] = limit;
    }
    if (cursor !== undefined) {
      queryParams['cursor'] = cursor;
    }
    const queryString = new URLSearchParams(queryParams).toString();
    if (queryString) {
      __path += '?' + queryString;
    }
    const config: RequestConfig = {
      method: 'GET',
      url: __path,
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["MemberListResponse"],
    };

    return this.request<types.MemberListResponse>(config);
  };

  /**
   * List organizations
   * Returns all organizations the authenticated user belongs to.
   */
  public readonly listOrganizations = async (limit?: number | undefined, cursor?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OrgListResponse> => {
    let __path = `/authsome/v1/orgs`;
    const queryParams: Record<string, any> = {};
    if (limit !== undefined) {
      queryParams['limit'] = limit;
    }
    if (cursor !== undefined) {
      queryParams['cursor'] = cursor;
    }
    const queryString = new URLSearchParams(queryParams).toString();
    if (queryString) {
      __path += '?' + queryString;
    }
    const config: RequestConfig = {
      method: 'GET',
      url: __path,
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["OrgListResponse"],
    };

    return this.request<types.OrgListResponse>(config);
  };

  /**
   * Remove member
   * Removes a member from an organization.
   */
  public readonly removeMember = async (orgId: string, memberId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OrganizationStatusResponse> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}/members/${encodeURIComponent(String(memberId))}`;
    const config: RequestConfig = {
      method: 'DELETE',
      url: __path,
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["OrganizationStatusResponse"],
    };

    return this.request<types.OrganizationStatusResponse>(config);
  };

  /**
   * Switch active organization
   * Sets the active organization on the caller's session. The user must be a member of the target org. An empty org_id clears the active org.
   */
  public readonly switchOrg = async (body: types.SwitchOrgRequest, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SwitchOrgResponse> => {
    let __path = `/authsome/v1/me/switch-org`;
    const config: RequestConfig = {
      method: 'POST',
      url: __path,
      body,
      bodyCodec: CODECS["SwitchOrgRequest"],
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["SwitchOrgResponse"],
    };

    return this.request<types.SwitchOrgResponse>(config);
  };

  /**
   * Update member role
   * Updates a member's role within an organization.
   */
  public readonly updateMember = async (orgId: string, memberId: string, body: types.UpdateMemberRequest, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Member> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}/members/${encodeURIComponent(String(memberId))}`;
    const config: RequestConfig = {
      method: 'PATCH',
      url: __path,
      body,
      bodyCodec: CODECS["UpdateMemberRequest"],
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["Member"],
    };

    return this.request<types.Member>(config);
  };

  /**
   * Update organization
   * Updates an organization's name or logo.
   */
  public readonly updateOrganization = async (orgId: string, body: types.UpdateOrgRequest, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Organization> => {
    let __path = `/authsome/v1/orgs/${encodeURIComponent(String(orgId))}`;
    const config: RequestConfig = {
      method: 'PATCH',
      url: __path,
      body,
      bodyCodec: CODECS["UpdateOrgRequest"],
      signal: options?.signal,
      retry: options?.retry,
      responseCodec: CODECS["Organization"],
    };

    return this.request<types.Organization>(config);
  };

}
