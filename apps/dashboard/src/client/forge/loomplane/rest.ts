import type { RequestConfig } from './fetch';
import { Client } from './client';
import * as types from './types';
import { CODECS } from './codecs';

export class RESTClient extends Client {
  public readonly activity = {
    /**
     * What the organization's Loomplane has done lately: deployments, plans, analyses and drift
     */
    get: async (days?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ActivitySummary> => {
      let __path = `/api/v1/activity`;
      const queryParams: Record<string, any> = {};
      if (days !== undefined) {
        queryParams['days'] = days;
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
        responseCodec: CODECS["ActivitySummary"],
      };

      return this.request<types.ActivitySummary>(config);
    },

  };

  public readonly analyses = {
    /**
     * Analyze the project's repositories, or the ones named
     */
    create: async (id: string, body: types.StartAnalysisInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.AnalysisList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/analyses`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["StartAnalysisInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["AnalysisList"],
      };

      return this.request<types.AnalysisList>(config);
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

  public readonly apiKeys = {
    /**
     * Make an API key that acts as you here. The secret is shown once.
     */
    create: async (body: types.NewKeyBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CreatedKey> => {
      let __path = `/api/v1/api-keys`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["NewKeyBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CreatedKey"],
      };

      return this.request<types.CreatedKey>(config);
    },

    /**
     * Your API keys in this organization, or everyone's for an owner or admin
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.APIKeyList> => {
      let __path = `/api/v1/api-keys`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["APIKeyList"],
      };

      return this.request<types.APIKeyList>(config);
    },

    /**
     * Revoke an API key, at once
     */
    revoke: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/api-keys/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        allowEmptyBody: true,
      };

      await this.request(config);
    },

  };

  public readonly bindings = {
    /**
     * What runs each of the project's services and resources in an environment
     */
    list: async (id: string, environment?: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.BindingList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/bindings`;
      const queryParams: Record<string, any> = {};
      queryParams['environment'] = environment;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["BindingList"],
      };

      return this.request<types.BindingList>(config);
    },

  };

  public readonly connectionClouds = {
    /**
     * The clouds you can connect, built in or from a provider plugin, and what each asks for
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudList> => {
      let __path = `/api/v1/connection-clouds`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudList"],
      };

      return this.request<types.CloudList>(config);
    },

  };

  public readonly connections = {
    /**
     * Ask the cloud again what the connection's credential may do
     */
    check: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(id))}/check`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Connection"],
      };

      return this.request<types.Connection>(config);
    },

    /**
     * Connect a Google Cloud project or a Vercel account, checking the credential with the cloud first
     */
    create: async (body: types.CreateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Connection"],
      };

      return this.request<types.Connection>(config);
    },

    /**
     * Remove a cloud connection and its credential
     */
    delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(id))}`;
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
     * One cloud connection
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Connection"],
      };

      return this.request<types.Connection>(config);
    },

    /**
     * The organization's cloud connections, never their credentials
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ConnectionList> => {
      let __path = `/api/v1/connections`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ConnectionList"],
      };

      return this.request<types.ConnectionList>(config);
    },

    /**
     * Replace the connection's credential, such as with a rotated key
     */
    replace: async (id: string, body: types.ReplaceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(id))}/credential`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["ReplaceInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Connection"],
      };

      return this.request<types.Connection>(config);
    },

  };

  public readonly deployed = {
    /**
     * The commit of each of the project's repositories deployed in the environment; one with nothing deployed there isn't listed, and a plan can't hold it
     */
    list: async (id: string, environment: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeployedList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(environment))}/deployed`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeployedList"],
      };

      return this.request<types.DeployedList>(config);
    },

  };

  public readonly deployments = {
    /**
     * The deployment an approved plan started
     */
    forPlan: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Deployment> => {
      let __path = `/api/v1/plans/${encodeURIComponent(String(id))}/deployment`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Deployment"],
      };

      return this.request<types.Deployment>(config);
    },

    /**
     * One deployment, with each step's progress
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Deployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Deployment"],
      };

      return this.request<types.Deployment>(config);
    },

    /**
     * The project's latest deployments, newest first, with each step's progress
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeploymentList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/deployments`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeploymentList"],
      };

      return this.request<types.DeploymentList>(config);
    },

    /**
     * Run a failed deployment's failed and skipped steps again, with the plan that was approved
     */
    retry: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Deployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Deployment"],
      };

      return this.request<types.Deployment>(config);
    },

  };

  public readonly environmentConnections = {
    /**
     * Choose the connection the environment deploys to a cloud with, or clear the choice
     */
    choose: async (id: string, environment: string, cloud: string, body: types.ChooseConnectionInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EnvironmentConnections> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(environment))}/connections/${encodeURIComponent(String(cloud))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["ChooseConnectionInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EnvironmentConnections"],
      };

      return this.request<types.EnvironmentConnections>(config);
    },

    /**
     * Which cloud connection the environment deploys to each cloud with, where someone chose
     */
    get: async (id: string, environment: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EnvironmentConnections> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(environment))}/connections`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EnvironmentConnections"],
      };

      return this.request<types.EnvironmentConnections>(config);
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

  public readonly manifests = {
    /**
     * The project's manifest: where its services run and what they need
     */
    current: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Revision> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/manifest`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Revision"],
      };

      return this.request<types.Revision>(config);
    },

    /**
     * A first manifest drafted from the newest successful analysis of each repository, to review before saving
     */
    draft: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ManifestDraft> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/manifest/draft`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ManifestDraft"],
      };

      return this.request<types.ManifestDraft>(config);
    },

    /**
     * Add a resource to the manifest, or ignore a resource kind, keeping the YAML's comments
     */
    edit: async (id: string, body: types.EditInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Revision> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/manifest/edits`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["EditInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Revision"],
      };

      return this.request<types.Revision>(config);
    },

    /**
     * The project's latest manifest revisions, newest first
     */
    history: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RevisionList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/manifest/revisions`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RevisionList"],
      };

      return this.request<types.RevisionList>(config);
    },

    /**
     * Save a new revision of the project's manifest, as YAML or as a parsed manifest
     */
    save: async (id: string, body: types.SaveInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Revision> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/manifest`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["SaveInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Revision"],
      };

      return this.request<types.Revision>(config);
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

  public readonly plans = {
    /**
     * Approve a ready plan, naming the revision you reviewed
     */
    approve: async (id: string, body: types.DecideInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Plan> => {
      let __path = `/api/v1/plans/${encodeURIComponent(String(id))}/approve`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["DecideInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Plan"],
      };

      return this.request<types.Plan>(config);
    },

    /**
     * Plan an environment from the current manifest and one commit of each repository, replacing its open plan; a scope moves only some of them
     */
    create: async (id: string, body: types.CreatePlanInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Plan> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/plans`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreatePlanInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Plan"],
      };

      return this.request<types.Plan>(config);
    },

    /**
     * One plan, with its actions and findings
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Plan> => {
      let __path = `/api/v1/plans/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Plan"],
      };

      return this.request<types.Plan>(config);
    },

    /**
     * The project's latest plans, newest first
     */
    list: async (id: string, environment?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlanList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/plans`;
      const queryParams: Record<string, any> = {};
      if (environment !== undefined) {
        queryParams['environment'] = environment;
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
        responseCodec: CODECS["PlanList"],
      };

      return this.request<types.PlanList>(config);
    },

    /**
     * Reject a ready plan
     */
    reject: async (id: string, body: types.DecideInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Plan> => {
      let __path = `/api/v1/plans/${encodeURIComponent(String(id))}/reject`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["DecideInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Plan"],
      };

      return this.request<types.Plan>(config);
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
     * The project's application graph, joined from the newest successful analysis of each repository
     */
    graph: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProjectView> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/graph`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProjectView"],
      };

      return this.request<types.ProjectView>(config);
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
     * Change a project's name, description, icon or color
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

  public readonly providers = {
    /**
     * The deployment targets a manifest can name, and what each runs and provisions
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProviderList> => {
      let __path = `/api/v1/providers`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProviderList"],
      };

      return this.request<types.ProviderList>(config);
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

  public readonly sources = {
    /**
     * Attach a repository, or a directory of one, after checking Loomplane can read it
     */
    create: async (id: string, body: types.AddSourceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Source> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/sources`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["AddSourceInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Source"],
      };

      return this.request<types.Source>(config);
    },

    /**
     * Detach a source. Its alias stays taken, so old plans keep their meaning.
     */
    delete: async (id: string, alias: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/sources/${encodeURIComponent(String(alias))}`;
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
     * The repositories the project builds from, by alias
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SourceList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/sources`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SourceList"],
      };

      return this.request<types.SourceList>(config);
    },

    /**
     * Change the directory or branch a source reads
     */
    update: async (id: string, alias: string, body: types.UpdateSourceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Source> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/sources/${encodeURIComponent(String(alias))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateSourceInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Source"],
      };

      return this.request<types.Source>(config);
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
