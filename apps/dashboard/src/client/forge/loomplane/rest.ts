import type { RequestConfig } from './fetch';
import { Client } from './client';
import * as types from './types';
import { CODECS } from './codecs';

export class RESTClient extends Client {
  public readonly environments = {
    /**
     * A project's environments
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EnvironmentList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EnvironmentList"],
      };

      return this.request<types.EnvironmentList>(config);
    },

  };

  public readonly infrastructure = {
    /**
     * What Ctrl Plane can deploy to
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.InfrastructureStatus> => {
      let __path = `/api/v1/infrastructure`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["InfrastructureStatus"],
      };

      return this.request<types.InfrastructureStatus>(config);
    },

  };

  public readonly invitations = {
    /**
     * Invite someone by email and get the link that lets them join
     */
    create: async (body: types.CreateInvitationInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CreatedInvitation> => {
      let __path = `/api/v1/invitations`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateInvitationInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CreatedInvitation"],
      };

      return this.request<types.CreatedInvitation>(config);
    },

    /**
     * Invitations nobody has accepted yet
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OrgInvitationList> => {
      let __path = `/api/v1/invitations`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["OrgInvitationList"],
      };

      return this.request<types.OrgInvitationList>(config);
    },

  };

  public readonly members = {
    /**
     * The organization's members, with their names and emails
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OrgMemberList> => {
      let __path = `/api/v1/members`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["OrgMemberList"],
      };

      return this.request<types.OrgMemberList>(config);
    },

  };

  public readonly projects = {
    /**
     * Create a project and start initializing it
     */
    create: async (body: types.CreateProjectInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Project> => {
      let __path = `/api/v1/projects`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateProjectInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Project"],
      };

      return this.request<types.Project>(config);
    },

    /**
     * Delete a project and its environments
     */
    delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        allowEmptyBody: true,
      };

      await this.request(config);
    },

    /**
     * One project
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Project> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Project"],
      };

      return this.request<types.Project>(config);
    },

    /**
     * The organization's projects, newest first
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProjectList> => {
      let __path = `/api/v1/projects`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProjectList"],
      };

      return this.request<types.ProjectList>(config);
    },

    /**
     * Change a project's name, description or repository
     */
    update: async (id: string, body: types.UpdateProjectInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Project> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateProjectInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Project"],
      };

      return this.request<types.Project>(config);
    },

  };

  public readonly publicConfig = {
    /**
     * Settings the dashboard needs before sign-in
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PublicConfig> => {
      let __path = `/api/v1/public-config`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PublicConfig"],
      };

      return this.request<types.PublicConfig>(config);
    },

  };

  public readonly session = {
    /**
     * The signed-in user, their active organization and their permissions in it
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Session> => {
      let __path = `/api/v1/session`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Session"],
      };

      return this.request<types.Session>(config);
    },

  };

}
