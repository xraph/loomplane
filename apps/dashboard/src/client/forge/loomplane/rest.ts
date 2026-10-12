import type { RequestConfig } from './fetch';
import { Client } from './client';
import * as types from './types';
import { CODECS } from './codecs';

export class RESTClient extends Client {
  public readonly analyses = {
    /**
     * Analyze the project's repository: find its services, packages and the resources they need
     */
    create: async (id: string, body: types.StartAnalysisInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Analysis> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/analyses`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["StartAnalysisInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Analysis"],
      };

      return this.request<types.Analysis>(config);
    },

    /**
     * One analysis
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Analysis> => {
      let __path = `/api/v1/analyses/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Analysis"],
      };

      return this.request<types.Analysis>(config);
    },

    /**
     * The application graph a succeeded analysis found, with the evidence for each finding
     */
    graph: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.AnalysisGraph> => {
      let __path = `/api/v1/analyses/${encodeURIComponent(String(id))}/graph`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["AnalysisGraph"],
      };

      return this.request<types.AnalysisGraph>(config);
    },

    /**
     * The project's latest analyses, newest first
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.AnalysisList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/analyses`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["AnalysisList"],
      };

      return this.request<types.AnalysisList>(config);
    },

  };

  public readonly environments = {
    /**
     * Add an environment to a project
     */
    create: async (id: string, body: types.CreateEnvironmentInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Environment> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateEnvironmentInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Environment"],
      };

      return this.request<types.Environment>(config);
    },

    /**
     * Delete an environment and the variables set on it. A project's last environment can't be deleted.
     */
    delete: async (id: string, envId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(envId))}`;
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

  public readonly github = {
    /**
     * Connect a GitHub App installation, from what GitHub's install redirect carried back
     */
    connect: async (body: types.ConnectGitHubInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GitHubInstallation> => {
      let __path = `/api/v1/github/installations`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ConnectGitHubInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GitHubInstallation"],
      };

      return this.request<types.GitHubInstallation>(config);
    },

    /**
     * Disconnect an installation. The App stays installed on GitHub.
     */
    disconnect: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/github/installations/${encodeURIComponent(String(id))}`;
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
     * The repositories the organization's installations can read
     */
    repositories: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ConnectedRepositoryList> => {
      let __path = `/api/v1/github/repositories`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ConnectedRepositoryList"],
      };

      return this.request<types.ConnectedRepositoryList>(config);
    },

    /**
     * The organization's GitHub connection, and a link to connect another account
     */
    status: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GitHubStatus> => {
      let __path = `/api/v1/github`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GitHubStatus"],
      };

      return this.request<types.GitHubStatus>(config);
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

  public readonly projectVariables = {
    /**
     * Set a variable for a project, or for one of its services
     */
    create: async (id: string, body: types.CreateVariableInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Variable> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/variables`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateVariableInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Variable"],
      };

      return this.request<types.Variable>(config);
    },

    /**
     * A project's own variables, project-wide and per service
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.VariableList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/variables`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["VariableList"],
      };

      return this.request<types.VariableList>(config);
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
     * Change a project's name, description, repository, icon or color
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

  public readonly variables = {
    /**
     * Set an organization-wide variable, for every environment or for one by name
     */
    create: async (body: types.CreateVariableInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Variable> => {
      let __path = `/api/v1/variables`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateVariableInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Variable"],
      };

      return this.request<types.Variable>(config);
    },

    /**
     * Delete a variable, and its secret
     */
    delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/variables/${encodeURIComponent(String(id))}`;
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
     * The organization-wide variables every project inherits
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.VariableList> => {
      let __path = `/api/v1/variables`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["VariableList"],
      };

      return this.request<types.VariableList>(config);
    },

    /**
     * What a project, or one of its services, gets in an environment, and which layer each value comes from
     */
    resolve: async (id: string, environment?: string, service?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Resolution> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/variables/resolved`;
      const queryParams: Record<string, any> = {};
      queryParams['environment'] = environment;
      if (service !== undefined) {
        queryParams['service'] = service;
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
        responseCodec: CODECS["Resolution"],
      };

      return this.request<types.Resolution>(config);
    },

    /**
     * Change a variable's value, or whether it's secret
     */
    update: async (id: string, body: types.UpdateVariableInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Variable> => {
      let __path = `/api/v1/variables/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateVariableInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Variable"],
      };

      return this.request<types.Variable>(config);
    },

  };

}
