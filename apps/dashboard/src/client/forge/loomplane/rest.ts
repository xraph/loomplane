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

  public readonly aiModelDeployments = {
    /**
     * Stop a model that's waiting for its GPU server; a server already approved stays in Infrastructure
     */
    cancel: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CancelResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/${encodeURIComponent(String(resource))}/waiting`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CancelResult"],
      };

      return this.request<types.CancelResult>(config);
    },

    /**
     * A model's state, cost, budget and the services that link to it
     */
    get: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ModelsStatus> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/${encodeURIComponent(String(resource))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ModelsStatus"],
      };

      return this.request<types.ModelsStatus>(config);
    },

    /**
     * Every model deployed in the projects you can read, with its state and cost
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ModelDeploymentList> => {
      let __path = `/api/v1/ai/models/deployments`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ModelDeploymentList"],
      };

      return this.request<types.ModelDeploymentList>(config);
    },

    /**
     * Move a model to another server, cluster or new GPU server and plan its environment
     * For a server size target, send server with the cloud_id, operation_id and review of the GPU server a preview with review_server true returned, within five minutes of that review. The deployment then waits for the server, and Loomplane plans the model onto it once it's up.
     */
    move: async (projectId: string, environmentId: string, resource: string, body: types.ModelsMoveInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeployResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/${encodeURIComponent(String(resource))}/move`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ModelsMoveInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeployResult"],
      };

      return this.request<types.DeployResult>(config);
    },

    /**
     * What moving a model to another server, cluster or new GPU server would do
     * Writes nothing, except the review of a GPU server when review_server is true, which only an admin may ask for. The rules are a deploy preview's.
     */
    movePreview: async (projectId: string, environmentId: string, resource: string, body: types.ModelsMoveInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeployPreview> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/${encodeURIComponent(String(resource))}/move-preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ModelsMoveInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeployPreview"],
      };

      return this.request<types.DeployPreview>(config);
    },

    /**
     * Move a model deployed before a registry re-pin to the weights the registry pins now, and plan its environment
     */
    moveToPin: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeployResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/${encodeURIComponent(String(resource))}/move-to-pin`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeployResult"],
      };

      return this.request<types.DeployResult>(config);
    },

    /**
     * Take a model out of the manifest and plan its environment; its weights stay on the server
     */
    remove: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeployResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/${encodeURIComponent(String(resource))}`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeployResult"],
      };

      return this.request<types.DeployResult>(config);
    },

    /**
     * Try a model that failed to start, or whose GPU server needs attention, again
     */
    retry: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ModelsStatus> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/${encodeURIComponent(String(resource))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ModelsStatus"],
      };

      return this.request<types.ModelsStatus>(config);
    },

    /**
     * Change a model's monthly budget or its hourly price, without changing what runs
     */
    update: async (projectId: string, environmentId: string, resource: string, body: types.ModelsUpdateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ModelsStatus> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/${encodeURIComponent(String(resource))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["ModelsUpdateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ModelsStatus"],
      };

      return this.request<types.ModelsStatus>(config);
    },

  };

  public readonly aiModels = {
    /**
     * A model's card from Hugging Face, as Markdown
     */
    card: async (id?: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Card> => {
      let __path = `/api/v1/ai/models/registry/card`;
      const queryParams: Record<string, any> = {};
      queryParams['id'] = id;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Card"],
      };

      return this.request<types.Card>(config);
    },

    /**
     * Deploy a model in this environment: add it to the manifest and plan the environment, or create its GPU server first
     * For a server size target, send server with the cloud_id, operation_id and review of the GPU server a preview with review_server true returned, within five minutes of that review. The deployment then waits for the server, and Loomplane plans the model onto it once it's up.
     */
    deploy: async (projectId: string, environmentId: string, body: types.DeployInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeployResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["DeployInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeployResult"],
      };

      return this.request<types.DeployResult>(config);
    },

    /**
     * One model with its quantizations and where each fits
     */
    entry: async (id?: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EntryDetail> => {
      let __path = `/api/v1/ai/models/registry/entry`;
      const queryParams: Record<string, any> = {};
      queryParams['id'] = id;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EntryDetail"],
      };

      return this.request<types.EntryDetail>(config);
    },

    /**
     * Read any Hugging Face model, pin it to a commit and work out where it fits
     */
    inspect: async (body: types.InspectInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EntryDetail> => {
      let __path = `/api/v1/ai/models/inspect`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["InspectInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EntryDetail"],
      };

      return this.request<types.EntryDetail>(config);
    },

    /**
     * What deploying a model in this environment would do, what it costs and what stops it
     * Writes nothing. A server size target is priced from its list price and comes back with server_review_needed and can_deploy false. Send the same body with review_server true to review the GPU server: only an admin may, and it records the review in Infrastructure and returns it as server, priced by the provider. Deploy then sends server set from it within five minutes.
     */
    preview: async (projectId: string, environmentId: string, body: types.DeployInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeployPreview> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/ai/models/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["DeployInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeployPreview"],
      };

      return this.request<types.DeployPreview>(config);
    },

    /**
     * The models Loomplane can deploy, filtered by task, size, license, fit and whether they were tested
     */
    registry: async (q?: string, task?: string, size?: string, license?: string, commercial?: boolean, fits?: boolean, tested?: boolean, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RegistryList> => {
      let __path = `/api/v1/ai/models/registry`;
      const queryParams: Record<string, any> = {};
      queryParams['q'] = q;
      queryParams['task'] = task;
      queryParams['size'] = size;
      queryParams['license'] = license;
      queryParams['commercial'] = commercial;
      queryParams['fits'] = fits;
      queryParams['tested'] = tested;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RegistryList"],
      };

      return this.request<types.RegistryList>(config);
    },

  };

  public readonly aiSettings = {
    /**
     * Delete the workspace's Hugging Face token, unless a deployed gated model downloads with it
     */
    deleteHuggingFaceToken: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/ai/settings/huggingface-token`;
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
     * The workspace's AI settings, without any secret, and whether you can change them
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.View> => {
      let __path = `/api/v1/ai/settings`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["View"],
      };

      return this.request<types.View>(config);
    },

    /**
     * Check a Hugging Face token with Hugging Face and save it as the workspace's; it's never shown again
     */
    putHuggingFaceToken: async (body: types.HuggingFaceTokenInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.HuggingFaceToken> => {
      let __path = `/api/v1/ai/settings/huggingface-token`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["HuggingFaceTokenInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["HuggingFaceToken"],
      };

      return this.request<types.HuggingFaceToken>(config);
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

  public readonly backupPolicies = {
    /**
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PolicyList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/backup-policies`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PolicyList"],
      };

      return this.request<types.PolicyList>(config);
    },

    /**
     */
    set: async (id: string, body: types.PolicyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Policy> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/backup-policies`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["PolicyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Policy"],
      };

      return this.request<types.Policy>(config);
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

  public readonly catalogAdmin = {
    /**
     * Open a catalog service's own admin through Loomplane's sandboxed proxy, for 30 minutes of idle time and at most 8 hours; needs permission to modify the project's infrastructure
     */
    open: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CatalogproxySession> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/catalog/${encodeURIComponent(String(resource))}/admin-sessions`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CatalogproxySession"],
      };

      return this.request<types.CatalogproxySession>(config);
    },

  };

  public readonly catalogEndpoints = {
    /**
     * Check a Postgres or Redis DSN from a target against what a kind's dependency needs, before storing it; the DSN isn't stored or returned
     */
    check: async (body: types.CheckInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CheckResult> => {
      let __path = `/api/v1/catalog/endpoints/checks`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CheckInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CheckResult"],
      };

      return this.request<types.CheckResult>(config);
    },

    /**
     * Register an external Postgres or Redis; its DSN is kept in Vault and never returned
     */
    create: async (body: types.CatalogconnectCreateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CatalogconnectEndpoint> => {
      let __path = `/api/v1/catalog/endpoints`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CatalogconnectCreateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CatalogconnectEndpoint"],
      };

      return this.request<types.CatalogconnectEndpoint>(config);
    },

    /**
     * Forget an external endpoint and its DSN; refused while a service connects to it. Nothing on the endpoint changes
     */
    delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/catalog/endpoints/${encodeURIComponent(String(id))}`;
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
     * The external databases and caches a catalog service can connect to, without their DSNs
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EndpointList> => {
      let __path = `/api/v1/catalog/endpoints`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EndpointList"],
      };

      return this.request<types.EndpointList>(config);
    },

  };

  public readonly catalogKinds = {
    /**
     * The catalog kinds a manifest can name: the database engines and the composed services, and where each can be placed and run
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CatalogKindList> => {
      let __path = `/api/v1/catalog/kinds`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CatalogKindList"],
      };

      return this.request<types.CatalogKindList>(config);
    },

  };

  public readonly cloudCapacity = {
    /**
     * cloudCapacity.approveExpansion
     */
    approveExpansion: async (cloudId: string, expansionId: string, body: types.ApproveExpansionInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Expansion> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/expansions/${encodeURIComponent(String(expansionId))}/approve`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApproveExpansionInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Expansion"],
      };

      return this.request<types.Expansion>(config);
    },

    /**
     * cloudCapacity.createScalingPool
     */
    createScalingPool: async (cloudId: string, body: types.ScalingPoolInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ScalingPool> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/scaling-pools`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ScalingPoolInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ScalingPool"],
      };

      return this.request<types.ScalingPool>(config);
    },

    /**
     * cloudCapacity.getPolicy
     */
    getPolicy: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CapacityPolicyView> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/capacity-policy`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CapacityPolicyView"],
      };

      return this.request<types.CapacityPolicyView>(config);
    },

    /**
     * cloudCapacity.listExpansions
     */
    listExpansions: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ExpansionList> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/expansions`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ExpansionList"],
      };

      return this.request<types.ExpansionList>(config);
    },

    /**
     * cloudCapacity.refresh
     */
    refresh: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CapacityUsage> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/capacity-usage/refresh`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CapacityUsage"],
      };

      return this.request<types.CapacityUsage>(config);
    },

    /**
     * cloudCapacity.savePolicy
     */
    savePolicy: async (cloudId: string, body: types.CapacityPolicyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CapacityPolicyView> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/capacity-policy`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CapacityPolicyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CapacityPolicyView"],
      };

      return this.request<types.CapacityPolicyView>(config);
    },

    /**
     * cloudCapacity.usage
     */
    usage: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CapacityUsage> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/capacity-usage`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CapacityUsage"],
      };

      return this.request<types.CapacityUsage>(config);
    },

  };

  public readonly cloudChanges = {
    /**
     * cloudChanges.apply
     */
    apply: async (id: string, body: types.ApplyCloudInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-changes/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyCloudInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

    /**
     * cloudChanges.getPreview
     */
    getPreview: async (id: string, previewId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preview> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-changes/${encodeURIComponent(String(previewId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Preview"],
      };

      return this.request<types.Preview>(config);
    },

    /**
     * cloudChanges.preview
     */
    preview: async (id: string, body: types.ChangeInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preview> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-changes/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ChangeInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Preview"],
      };

      return this.request<types.Preview>(config);
    },

  };

  public readonly cloudPlacements = {
    /**
     * cloudPlacements.acceptMove
     */
    acceptMove: async (cloudId: string, body: types.MoveInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.MoveReview> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/placements/accept-move`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["MoveInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["MoveReview"],
      };

      return this.request<types.MoveReview>(config);
    },

    /**
     * cloudPlacements.previewMove
     */
    previewMove: async (cloudId: string, body: types.MoveInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.MoveReview> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/placements/preview-move`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["MoveInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["MoveReview"],
      };

      return this.request<types.MoveReview>(config);
    },

    /**
     */
    previewRecovery: async (cloudId: string, body: types.RecoveryInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ReservationRecovery> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/placements/preview-recovery`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RecoveryInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ReservationRecovery"],
      };

      return this.request<types.ReservationRecovery>(config);
    },

    /**
     */
    recover: async (cloudId: string, body: types.RecoveryInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ReservationRecovery> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/placements/recover`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RecoveryInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ReservationRecovery"],
      };

      return this.request<types.ReservationRecovery>(config);
    },

  };

  public readonly cloudPools = {
    /**
     * cloudPools.create
     */
    create: async (cloudId: string, body: types.PoolInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Pool> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/pools`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["PoolInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Pool"],
      };

      return this.request<types.Pool>(config);
    },

    /**
     * cloudPools.list
     */
    list: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PoolList> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/pools`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PoolList"],
      };

      return this.request<types.PoolList>(config);
    },

    /**
     * cloudPools.update
     */
    update: async (cloudId: string, poolId: string, body: types.PoolInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Pool> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/pools/${encodeURIComponent(String(poolId))}`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["PoolInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Pool"],
      };

      return this.request<types.Pool>(config);
    },

  };

  public readonly cloudServers = {
    /**
     * cloudServers.applyDrain
     */
    applyDrain: async (cloudId: string, serverId: string, body: types.ApplyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Server> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/servers/${encodeURIComponent(String(serverId))}/drain/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Server"],
      };

      return this.request<types.Server>(config);
    },

    /**
     * cloudServers.check
     */
    check: async (cloudId: string, serverId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Server> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/servers/${encodeURIComponent(String(serverId))}/check`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Server"],
      };

      return this.request<types.Server>(config);
    },

    /**
     * cloudServers.list
     */
    list: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ServerList> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/servers`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ServerList"],
      };

      return this.request<types.ServerList>(config);
    },

    /**
     * cloudServers.options
     */
    options: async (cloudId: string, connectionId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ServerOptions> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/server-options/${encodeURIComponent(String(connectionId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ServerOptions"],
      };

      return this.request<types.ServerOptions>(config);
    },

    /**
     * cloudServers.preview
     */
    preview: async (cloudId: string, body: types.ServerInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Operation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/servers/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ServerInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Operation"],
      };

      return this.request<types.Operation>(config);
    },

    /**
     * cloudServers.previewDrain
     */
    previewDrain: async (cloudId: string, serverId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DrainReview> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/servers/${encodeURIComponent(String(serverId))}/drain/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DrainReview"],
      };

      return this.request<types.DrainReview>(config);
    },

    /**
     * cloudServers.usage
     */
    usage: async (cloudId: string, serverId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ServerUsage> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/servers/${encodeURIComponent(String(serverId))}/usage`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ServerUsage"],
      };

      return this.request<types.ServerUsage>(config);
    },

  };

  public readonly cloudSignInApps = {
    /**
     * Remove the organization's own sign-in app for a cloud
     */
    delete: async (cloud: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/cloud-sign-in/apps/${encodeURIComponent(String(cloud))}`;
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
     * The clouds you can sign in to, where each one's app comes from, and the callback URL to register
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SignInAppList> => {
      let __path = `/api/v1/cloud-sign-in/apps`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SignInAppList"],
      };

      return this.request<types.SignInAppList>(config);
    },

    /**
     * Set the organization's own sign-in app for a cloud. The secret is never returned
     */
    set: async (cloud: string, body: types.AppInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SignInApp> => {
      let __path = `/api/v1/cloud-sign-in/apps/${encodeURIComponent(String(cloud))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["AppInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SignInApp"],
      };

      return this.request<types.SignInApp>(config);
    },

  };

  public readonly cloudSyncs = {
    /**
     * cloudSyncs.applyRevert
     */
    applyRevert: async (id: string, body: types.ApplyCloudInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-syncs/revert/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyCloudInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

    /**
     * cloudSyncs.completeRouting
     */
    completeRouting: async (id: string, syncId: string, body: types.CompleteCloudRoutingInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-syncs/${encodeURIComponent(String(syncId))}/routing`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CompleteCloudRoutingInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

    /**
     * cloudSyncs.get
     */
    get: async (id: string, syncId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-syncs/${encodeURIComponent(String(syncId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

    /**
     * cloudSyncs.list
     */
    list: async (id: string, before?: string | undefined, limit?: number | undefined, cloud?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudSyncList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-syncs`;
      const queryParams: Record<string, any> = {};
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (cloud !== undefined) {
        queryParams['cloud'] = cloud;
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
        responseCodec: CODECS["CloudSyncList"],
      };

      return this.request<types.CloudSyncList>(config);
    },

    /**
     * cloudSyncs.previewRevert
     */
    previewRevert: async (id: string, syncId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preview> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-syncs/${encodeURIComponent(String(syncId))}/revert/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Preview"],
      };

      return this.request<types.Preview>(config);
    },

    /**
     * cloudSyncs.retry
     */
    retry: async (id: string, syncId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/cloud-syncs/${encodeURIComponent(String(syncId))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

  };

  public readonly cloudTargets = {
    /**
     * cloudTargets.create
     */
    create: async (id: string, cloudId: string, body: types.CreateTargetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Target> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/targets`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateTargetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Target"],
      };

      return this.request<types.Target>(config);
    },

    /**
     * cloudTargets.delete
     */
    delete: async (id: string, cloudId: string, targetId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
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
     * cloudTargets.get
     */
    get: async (id: string, cloudId: string, targetId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Target> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Target"],
      };

      return this.request<types.Target>(config);
    },

    /**
     * cloudTargets.list
     */
    list: async (id: string, cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudTargetList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/targets`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudTargetList"],
      };

      return this.request<types.CloudTargetList>(config);
    },

    /**
     * cloudTargets.update
     */
    update: async (id: string, cloudId: string, targetId: string, body: types.UpdateTargetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Target> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateTargetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Target"],
      };

      return this.request<types.Target>(config);
    },

  };

  public readonly clouds = {
    /**
     * clouds.create
     */
    create: async (id: string, body: types.CloudprofilesCreateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CloudprofilesCreateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Profile"],
      };

      return this.request<types.Profile>(config);
    },

    /**
     * clouds.delete
     */
    delete: async (id: string, cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}`;
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
     * clouds.get
     */
    get: async (id: string, cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Profile"],
      };

      return this.request<types.Profile>(config);
    },

    /**
     * clouds.list
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlanningCloudList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PlanningCloudList"],
      };

      return this.request<types.PlanningCloudList>(config);
    },

    /**
     * clouds.setDefaultPlacement
     */
    setDefaultPlacement: async (id: string, cloudId: string, body: types.SetDefaultPlacementInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/default-placement`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["SetDefaultPlacementInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Profile"],
      };

      return this.request<types.Profile>(config);
    },

    /**
     * clouds.update
     */
    update: async (id: string, cloudId: string, body: types.UpdateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Profile"],
      };

      return this.request<types.Profile>(config);
    },

    /**
     * clouds.usage
     */
    usage: async (id: string, cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Usage> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/usage`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Usage"],
      };

      return this.request<types.Usage>(config);
    },

  };

  public readonly connectionClouds = {
    /**
     * The clouds you can connect, built in or from a provider plugin, and what each asks for
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ConnectionsCloudList> => {
      let __path = `/api/v1/connection-clouds`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ConnectionsCloudList"],
      };

      return this.request<types.ConnectionsCloudList>(config);
    },

  };

  public readonly connectionGpus = {
    /**
     * GPUs a server or cluster connection can see, read live
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GPUInventory> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(id))}/gpus`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GPUInventory"],
      };

      return this.request<types.GPUInventory>(config);
    },

  };

  public readonly connectionInCluster = {
    /**
     * Connect the Kubernetes cluster Loomplane runs in, as Loomplane's own service account, checking the namespace first
     */
    create: async (body: types.InClusterInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections/in-cluster`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["InClusterInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Connection"],
      };

      return this.request<types.Connection>(config);
    },

  };

  public readonly connectionKeyless = {
    /**
     * Connect a cloud keylessly, with the reserved ID, once the cloud accepts Loomplane's token
     */
    create: async (body: types.KeylessInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections/keyless`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["KeylessInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Connection"],
      };

      return this.request<types.Connection>(config);
    },

    /**
     * Reserve a new keyless connection's ID for a day, and say how to set up the cloud's trust in it
     */
    prepare: async (body: types.KeylessPrepareInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.KeylessSetup> => {
      let __path = `/api/v1/connections/keyless/prepare`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["KeylessPrepareInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["KeylessSetup"],
      };

      return this.request<types.KeylessSetup>(config);
    },

    /**
     * Move a connection to keyless, or change its keyless settings, if the cloud's trust reaches the same account
     */
    replace: async (id: string, body: types.KeylessReplaceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(id))}/keyless`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["KeylessReplaceInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Connection"],
      };

      return this.request<types.Connection>(config);
    },

    /**
     * How to set up the cloud's trust in an existing connection, to move it to keyless
     */
    setup: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.KeylessSetup> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(id))}/keyless`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["KeylessSetup"],
      };

      return this.request<types.KeylessSetup>(config);
    },

  };

  public readonly connectionSignIns = {
    /**
     * Finish a sign-in with what the cloud sent back to the callback
     */
    complete: async (body: types.CompleteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections/sign-ins/complete`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CompleteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Connection"],
      };

      return this.request<types.Connection>(config);
    },

    /**
     * Start signing in to a cloud, for a new connection or in place of an existing one's credential
     */
    start: async (body: types.SignInInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SignIn> => {
      let __path = `/api/v1/connections/sign-ins`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["SignInInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SignIn"],
      };

      return this.request<types.SignIn>(config);
    },

  };

  public readonly connectionStorage = {
    /**
     * Owned persistent volumes and current attachment state
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.StorageVolumeList> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(id))}/storage`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["StorageVolumeList"],
      };

      return this.request<types.StorageVolumeList>(config);
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
    create: async (body: types.ConnectionsCreateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Connection> => {
      let __path = `/api/v1/connections`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ConnectionsCreateInputBody"],
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

  public readonly dbGateways = {
    /**
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GatewayList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/db-gateways`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GatewayList"],
      };

      return this.request<types.GatewayList>(config);
    },

    /**
     */
    pauseRoute: async (id: string, gatewayId: string, routeId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteSummary> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/db-gateways/${encodeURIComponent(String(gatewayId))}/routes/${encodeURIComponent(String(routeId))}/pause`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteSummary"],
      };

      return this.request<types.RouteSummary>(config);
    },

    /**
     */
    resumeRoute: async (id: string, gatewayId: string, routeId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteSummary> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/db-gateways/${encodeURIComponent(String(gatewayId))}/routes/${encodeURIComponent(String(routeId))}/resume`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteSummary"],
      };

      return this.request<types.RouteSummary>(config);
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
     * The deployment's canary: where traffic is and every step's verdict
     */
    canary: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CanaryReport> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/canary`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CanaryReport"],
      };

      return this.request<types.CanaryReport>(config);
    },

    /**
     * Cancel a deployment paused waiting for capacity; steps that finished stay as they are
     */
    cancelCapacity: async (id: string, body: types.CapacityWaitInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Deployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/capacity/cancel`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CapacityWaitInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Deployment"],
      };

      return this.request<types.Deployment>(config);
    },

    /**
     */
    cleanup: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.FailedCleanup> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/cleanup`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["FailedCleanup"],
      };

      return this.request<types.FailedCleanup>(config);
    },

    /**
     */
    cleanupApply: async (id: string, body: types.FailedCleanupInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.FailedCleanup> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/cleanup/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["FailedCleanupInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["FailedCleanup"],
      };

      return this.request<types.FailedCleanup>(config);
    },

    /**
     */
    cleanupPreview: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CleanupReview> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/cleanup/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CleanupReview"],
      };

      return this.request<types.CleanupReview>(config);
    },

    /**
     */
    cleanupRetry: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.FailedCleanup> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/cleanup/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["FailedCleanup"],
      };

      return this.request<types.FailedCleanup>(config);
    },

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
    list: async (id: string, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeploymentList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/deployments`;
      const queryParams: Record<string, any> = {};
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
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
        responseCodec: CODECS["DeploymentList"],
      };

      return this.request<types.DeploymentList>(config);
    },

    /**
     */
    logs: async (id: string, cursor?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.LogPage> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/logs`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
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
        responseCodec: CODECS["LogPage"],
      };

      return this.request<types.LogPage>(config);
    },

    /**
     * Check capacity again for a deployment paused waiting for it; the deployment keeps its steps and retries
     */
    resumeCapacity: async (id: string, body: types.CapacityWaitInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Deployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/capacity/resume`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CapacityWaitInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Deployment"],
      };

      return this.request<types.Deployment>(config);
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

  public readonly dnsZones = {
    /**
     * dnsZones.create
     */
    create: async (body: types.ZoneInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Zone> => {
      let __path = `/api/v1/dns-zones`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ZoneInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Zone"],
      };

      return this.request<types.Zone>(config);
    },

    /**
     * dnsZones.discover
     */
    discover: async (connectionId?: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProviderZoneList> => {
      let __path = `/api/v1/dns-zones/discover`;
      const queryParams: Record<string, any> = {};
      queryParams['connection_id'] = connectionId;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProviderZoneList"],
      };

      return this.request<types.ProviderZoneList>(config);
    },

    /**
     * dnsZones.get
     */
    get: async (zoneId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DomainDetail> => {
      let __path = `/api/v1/dns-zones/${encodeURIComponent(String(zoneId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DomainDetail"],
      };

      return this.request<types.DomainDetail>(config);
    },

    /**
     * dnsZones.list
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ZoneList> => {
      let __path = `/api/v1/dns-zones`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ZoneList"],
      };

      return this.request<types.ZoneList>(config);
    },

    /**
     * dnsZones.project
     */
    project: async (projectId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProjectZoneList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/dns-zones`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProjectZoneList"],
      };

      return this.request<types.ProjectZoneList>(config);
    },

    /**
     * dnsZones.remove
     */
    remove: async (zoneId: string, body: types.RemoveRouteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/dns-zones/${encodeURIComponent(String(zoneId))}/remove`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RemoveRouteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        allowEmptyBody: true,
      };

      await this.request(config);
    },

    /**
     * dnsZones.setChallengeCredential
     */
    setChallengeCredential: async (zoneId: string, body: types.ChallengeCredentialInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Zone> => {
      let __path = `/api/v1/dns-zones/${encodeURIComponent(String(zoneId))}/challenge-credential`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ChallengeCredentialInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Zone"],
      };

      return this.request<types.Zone>(config);
    },

    /**
     * dnsZones.update
     */
    update: async (zoneId: string, body: types.ZoneUpdateBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Zone> => {
      let __path = `/api/v1/dns-zones/${encodeURIComponent(String(zoneId))}/update`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ZoneUpdateBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Zone"],
      };

      return this.request<types.Zone>(config);
    },

    /**
     * dnsZones.verify
     */
    verify: async (zoneId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Zone> => {
      let __path = `/api/v1/dns-zones/${encodeURIComponent(String(zoneId))}/verify`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Zone"],
      };

      return this.request<types.Zone>(config);
    },

  };

  public readonly domainRegistration = {
    /**
     * domainRegistration.check
     */
    check: async (body: types.DomainCheckInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DomainOfferList> => {
      let __path = `/api/v1/domain-registration/check`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["DomainCheckInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DomainOfferList"],
      };

      return this.request<types.DomainOfferList>(config);
    },

    /**
     * domainRegistration.extensions
     */
    extensions: async (connectionId?: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RegistrationExtensionList> => {
      let __path = `/api/v1/domain-registration/extensions`;
      const queryParams: Record<string, any> = {};
      queryParams['connection_id'] = connectionId;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RegistrationExtensionList"],
      };

      return this.request<types.RegistrationExtensionList>(config);
    },

    /**
     * domainRegistration.search
     */
    search: async (connectionId?: string, q?: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DomainOfferList> => {
      let __path = `/api/v1/domain-registration/search`;
      const queryParams: Record<string, any> = {};
      queryParams['connection_id'] = connectionId;
      queryParams['q'] = q;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DomainOfferList"],
      };

      return this.request<types.DomainOfferList>(config);
    },

  };

  public readonly domainRegistrations = {
    /**
     * domainRegistrations.approve
     */
    approve: async (registrationId: string, body: types.RegistrationApprovalBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Registration> => {
      let __path = `/api/v1/domain-registrations/${encodeURIComponent(String(registrationId))}/approve`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RegistrationApprovalBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Registration"],
      };

      return this.request<types.Registration>(config);
    },

    /**
     * domainRegistrations.get
     */
    get: async (registrationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Registration> => {
      let __path = `/api/v1/domain-registrations/${encodeURIComponent(String(registrationId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Registration"],
      };

      return this.request<types.Registration>(config);
    },

    /**
     * domainRegistrations.list
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RegistrationList> => {
      let __path = `/api/v1/domain-registrations`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RegistrationList"],
      };

      return this.request<types.RegistrationList>(config);
    },

    /**
     * domainRegistrations.preview
     */
    preview: async (body: types.RegistrationInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Registration> => {
      let __path = `/api/v1/domain-registrations/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RegistrationInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Registration"],
      };

      return this.request<types.Registration>(config);
    },

    /**
     * domainRegistrations.setAutoRenew
     */
    setAutoRenew: async (registrationId: string, body: types.AutoRenewInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Registration> => {
      let __path = `/api/v1/domain-registrations/${encodeURIComponent(String(registrationId))}/auto-renew`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["AutoRenewInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Registration"],
      };

      return this.request<types.Registration>(config);
    },

  };

  public readonly domainRoutes = {
    /**
     * domainRoutes.apply
     */
    apply: async (projectId: string, operationId: string, body: types.ApplyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/route-operations/${encodeURIComponent(String(operationId))}/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.cutover
     */
    cutover: async (projectId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/route-operations/${encodeURIComponent(String(operationId))}/cutover`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.history
     */
    history: async (projectId: string, routeId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DomainOperationList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/operations`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DomainOperationList"],
      };

      return this.request<types.DomainOperationList>(config);
    },

    /**
     * domainRoutes.list
     */
    list: async (projectId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DomainRouteList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DomainRouteList"],
      };

      return this.request<types.DomainRouteList>(config);
    },

    /**
     * domainRoutes.operation
     */
    operation: async (projectId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/route-operations/${encodeURIComponent(String(operationId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.prepareHandoff
     */
    prepareHandoff: async (projectId: string, routeId: string, body: types.HandoffInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/prepare-handoff`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["HandoffInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.preview
     */
    preview: async (projectId: string, body: types.RouteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RouteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.releaseHandoff
     */
    releaseHandoff: async (projectId: string, operationId: string, body: types.HandoffReleaseInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/route-operations/${encodeURIComponent(String(operationId))}/release-handoff`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["HandoffReleaseInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.remove
     */
    remove: async (projectId: string, routeId: string, body: types.RemoveRouteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/remove`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RemoveRouteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.retry
     */
    retry: async (projectId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/route-operations/${encodeURIComponent(String(operationId))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.rollback
     */
    rollback: async (projectId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/route-operations/${encodeURIComponent(String(operationId))}/rollback`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

    /**
     * domainRoutes.status
     */
    status: async (projectId: string, routeId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudinfraRouteStatus> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/status`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudinfraRouteStatus"],
      };

      return this.request<types.CloudinfraRouteStatus>(config);
    },

    /**
     * domainRoutes.updateTTL
     */
    updateTTL: async (projectId: string, routeId: string, body: types.RouteTTLInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudinfraRoute> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/ttl`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RouteTTLInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudinfraRoute"],
      };

      return this.request<types.CloudinfraRoute>(config);
    },

    /**
     * domainRoutes.verify
     */
    verify: async (projectId: string, routeId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/verify`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteOperation"],
      };

      return this.request<types.RouteOperation>(config);
    },

  };

  public readonly domains = {
    /**
     * domains.overview
     */
    overview: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DomainOverview> => {
      let __path = `/api/v1/domains`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DomainOverview"],
      };

      return this.request<types.DomainOverview>(config);
    },

    /**
     * domains.project
     */
    project: async (projectId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProjectDomainList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domains`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProjectDomainList"],
      };

      return this.request<types.ProjectDomainList>(config);
    },

  };

  public readonly environmentClouds = {
    /**
     * environmentClouds.get
     */
    get: async (id: string, environmentId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EnvironmentCloud> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(environmentId))}/cloud`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EnvironmentCloud"],
      };

      return this.request<types.EnvironmentCloud>(config);
    },

    /**
     * environmentClouds.preview
     */
    preview: async (id: string, environmentId: string, body: types.EnvironmentCloudInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preview> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(environmentId))}/cloud/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["EnvironmentCloudInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Preview"],
      };

      return this.request<types.Preview>(config);
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

  public readonly fabriq = {
    /**
     * A hosted Fabriq's stack: its release, entity types and migrations, and whether its console answers
     */
    status: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Status> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Status"],
      };

      return this.request<types.Status>(config);
    },

  };

  public readonly fabriqKeys = {
    /**
     * The Fabriq key of each service that needs it or had one; never a key itself, only its prefix
     */
    list: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.FabriqKeyList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/keys`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["FabriqKeyList"],
      };

      return this.request<types.FabriqKeyList>(config);
    },

    /**
     * Revoke a service's Fabriq keys now; its next release gets a new one while it still needs Fabriq
     */
    revoke: async (projectId: string, environmentId: string, resource: string, service: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ConsumerKey> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/keys/${encodeURIComponent(String(service))}/revoke`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ConsumerKey"],
      };

      return this.request<types.ConsumerKey>(config);
    },

    /**
     * Issue a service a new Fabriq key, which its next release takes and then revokes the old one
     */
    rotate: async (projectId: string, environmentId: string, resource: string, service: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ConsumerKey> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/keys/${encodeURIComponent(String(service))}/rotate`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ConsumerKey"],
      };

      return this.request<types.ConsumerKey>(config);
    },

  };

  public readonly fabriqRebuilds = {
    /**
     * Rebuild a FalkorDB or Elasticsearch projection from Postgres; while one is queued or running, return it
     */
    create: async (projectId: string, environmentId: string, resource: string, body: types.RebuildInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Rebuild> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/rebuilds`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RebuildInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Rebuild"],
      };

      return this.request<types.Rebuild>(config);
    },

    /**
     * A hosted Fabriq's latest projection rebuilds, newest first
     */
    list: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RebuildList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/rebuilds`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RebuildList"],
      };

      return this.request<types.RebuildList>(config);
    },

  };

  public readonly fabriqTenants = {
    /**
     * Create a tenant in a hosted Fabriq, or find it
     */
    create: async (projectId: string, environmentId: string, resource: string, body: types.TenantInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Tenant> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/tenants`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["TenantInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Tenant"],
      };

      return this.request<types.Tenant>(config);
    },

    /**
     * A hosted Fabriq's tenants, by id
     */
    list: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.TenantList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/tenants`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["TenantList"],
      };

      return this.request<types.TenantList>(config);
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

  public readonly gpuTargets = {
    /**
     * Where GPUs can run and what they cost per hour
     */
    list: async (minMemoryGib?: number | undefined, count?: number | undefined, kind?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GPUTargetList> => {
      let __path = `/api/v1/gpu-targets`;
      const queryParams: Record<string, any> = {};
      if (minMemoryGib !== undefined) {
        queryParams['min_memory_gib'] = minMemoryGib;
      }
      if (count !== undefined) {
        queryParams['count'] = count;
      }
      if (kind !== undefined) {
        queryParams['kind'] = kind;
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
        responseCodec: CODECS["GPUTargetList"],
      };

      return this.request<types.GPUTargetList>(config);
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

  public readonly infrastructureEnrollment = {
    /**
     * Verify one-time server enrollment
     */
    complete: async (orgId: string, operationId: string, body: types.EnrollmentBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/infrastructure/enroll/${encodeURIComponent(String(orgId))}/${encodeURIComponent(String(operationId))}`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["EnrollmentBody"],
        signal: options?.signal,
        retry: options?.retry,
        allowEmptyBody: true,
      };

      await this.request(config);
    },

  };

  public readonly infrastructureOperations = {
    /**
     * infrastructureOperations.apply
     */
    apply: async (cloudId: string, operationId: string, body: types.ApplyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Operation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/infrastructure-operations/${encodeURIComponent(String(operationId))}/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Operation"],
      };

      return this.request<types.Operation>(config);
    },

    /**
     * infrastructureOperations.cancel
     */
    cancel: async (cloudId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Operation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/infrastructure-operations/${encodeURIComponent(String(operationId))}/cancel`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Operation"],
      };

      return this.request<types.Operation>(config);
    },

    /**
     * infrastructureOperations.get
     */
    get: async (cloudId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Operation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/infrastructure-operations/${encodeURIComponent(String(operationId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Operation"],
      };

      return this.request<types.Operation>(config);
    },

    /**
     * infrastructureOperations.list
     */
    list: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OperationList> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/infrastructure-operations`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["OperationList"],
      };

      return this.request<types.OperationList>(config);
    },

    /**
     * infrastructureOperations.retry
     */
    retry: async (cloudId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Operation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/infrastructure-operations/${encodeURIComponent(String(operationId))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Operation"],
      };

      return this.request<types.Operation>(config);
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

  public readonly nativeRecoveryCopies = {
    /**
     */
    cleanup: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRestore> => {
      let __path = `/api/v1/native-recovery-copies/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeRestore"],
      };

      return this.request<types.NativeRestore>(config);
    },

    /**
     */
    create: async (body: types.NativeCopyCreateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRestore> => {
      let __path = `/api/v1/native-recovery-copies`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["NativeCopyCreateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeRestore"],
      };

      return this.request<types.NativeRestore>(config);
    },

    /**
     */
    credentials: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeCopyCredentials> => {
      let __path = `/api/v1/native-recovery-copies/${encodeURIComponent(String(id))}/credentials`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeCopyCredentials"],
      };

      return this.request<types.NativeCopyCredentials>(config);
    },

    /**
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRestore> => {
      let __path = `/api/v1/native-recovery-copies/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeRestore"],
      };

      return this.request<types.NativeRestore>(config);
    },

    /**
     */
    list: async (id: string, environmentId?: string | undefined, before?: string | undefined, limit?: number | undefined, resource?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRecoveryCopies> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/native-recovery-copies`;
      const queryParams: Record<string, any> = {};
      if (environmentId !== undefined) {
        queryParams['environment_id'] = environmentId;
      }
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (resource !== undefined) {
        queryParams['resource'] = resource;
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
        responseCodec: CODECS["NativeRecoveryCopies"],
      };

      return this.request<types.NativeRecoveryCopies>(config);
    },

    /**
     */
    review: async (id: string, pointId: string, body: types.NativeCopyReviewInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeCopyReview> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/points/${encodeURIComponent(String(pointId))}/copy-review`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["NativeCopyReviewInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeCopyReview"],
      };

      return this.request<types.NativeCopyReview>(config);
    },

  };

  public readonly nativeRecoveryRetirements = {
    /**
     */
    create: async (id: string, pointId: string, body: types.NativeRetirementInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRetirement> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/points/${encodeURIComponent(String(pointId))}/retirements`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["NativeRetirementInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeRetirement"],
      };

      return this.request<types.NativeRetirement>(config);
    },

    /**
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRetirement> => {
      let __path = `/api/v1/native-recovery-retirements/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeRetirement"],
      };

      return this.request<types.NativeRetirement>(config);
    },

    /**
     */
    history: async (id: string, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetirementHistory> => {
      let __path = `/api/v1/native-recovery-retirements/${encodeURIComponent(String(id))}/history`;
      const queryParams: Record<string, any> = {};
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
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
        responseCodec: CODECS["RetirementHistory"],
      };

      return this.request<types.RetirementHistory>(config);
    },

    /**
     */
    list: async (id: string, environmentId?: string | undefined, before?: string | undefined, limit?: number | undefined, resource?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRetirementPage> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/retirements`;
      const queryParams: Record<string, any> = {};
      if (environmentId !== undefined) {
        queryParams['environment_id'] = environmentId;
      }
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (resource !== undefined) {
        queryParams['resource'] = resource;
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
        responseCodec: CODECS["NativeRetirementPage"],
      };

      return this.request<types.NativeRetirementPage>(config);
    },

    /**
     */
    retry: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRetirement> => {
      let __path = `/api/v1/native-recovery-retirements/${encodeURIComponent(String(id))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeRetirement"],
      };

      return this.request<types.NativeRetirement>(config);
    },

  };

  public readonly nativeRecoverySources = {
    /**
     */
    inventory: async (id: string, environmentId?: string | undefined, before?: string | undefined, limit?: number | undefined, resource?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeInventory> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}`;
      const queryParams: Record<string, any> = {};
      if (environmentId !== undefined) {
        queryParams['environment_id'] = environmentId;
      }
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (resource !== undefined) {
        queryParams['resource'] = resource;
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
        responseCodec: CODECS["NativeInventory"],
      };

      return this.request<types.NativeInventory>(config);
    },

    /**
     */
    list: async (id: string, environmentId?: string | undefined, before?: string | undefined, limit?: number | undefined, resource?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRecoverySources> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/native-recovery-sources`;
      const queryParams: Record<string, any> = {};
      if (environmentId !== undefined) {
        queryParams['environment_id'] = environmentId;
      }
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (resource !== undefined) {
        queryParams['resource'] = resource;
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
        responseCodec: CODECS["NativeRecoverySources"],
      };

      return this.request<types.NativeRecoverySources>(config);
    },

    /**
     */
    pitrWindow: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativePITRWindow> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/pitr-window`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativePITRWindow"],
      };

      return this.request<types.NativePITRWindow>(config);
    },

    /**
     */
    previewRetention: async (id: string, olderThanDays?: number, keepLast?: number, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetentionPreview> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/retention-preview`;
      const queryParams: Record<string, any> = {};
      queryParams['older_than_days'] = olderThanDays;
      queryParams['keep_last'] = keepLast;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RetentionPreview"],
      };

      return this.request<types.RetentionPreview>(config);
    },

    /**
     */
    refresh: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeInventory> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/refresh`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeInventory"],
      };

      return this.request<types.NativeInventory>(config);
    },

  };

  public readonly nativeRetentionPolicies = {
    /**
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRetentionPolicyState> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/retention-policy`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeRetentionPolicyState"],
      };

      return this.request<types.NativeRetentionPolicyState>(config);
    },

    /**
     */
    history: async (id: string, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetentionPolicyHistory> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/retention-policy/history`;
      const queryParams: Record<string, any> = {};
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
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
        responseCodec: CODECS["RetentionPolicyHistory"],
      };

      return this.request<types.RetentionPolicyHistory>(config);
    },

    /**
     */
    set: async (id: string, body: types.RetentionPolicyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NativeRetentionPolicy> => {
      let __path = `/api/v1/native-recovery-sources/${encodeURIComponent(String(id))}/retention-policy`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["RetentionPolicyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NativeRetentionPolicy"],
      };

      return this.request<types.NativeRetentionPolicy>(config);
    },

  };

  public readonly notifications = {
    /**
     * Recent deployment notifications
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NotificationList> => {
      let __path = `/api/v1/notifications`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NotificationList"],
      };

      return this.request<types.NotificationList>(config);
    },

    preferences: {
      /**
       * Your deployment email preference
       */
      get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preferences> => {
        let __path = `/api/v1/notifications/preferences`;
        const config: RequestConfig = {
          method: 'GET',
          url: __path,
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["Preferences"],
        };

        return this.request<types.Preferences>(config);
      },

      /**
       * Change your deployment email preference
       */
      update: async (body: types.PreferenceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preferences> => {
        let __path = `/api/v1/notifications/preferences`;
        const config: RequestConfig = {
          method: 'PUT',
          url: __path,
          body,
          bodyCodec: CODECS["PreferenceInputBody"],
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["Preferences"],
        };

        return this.request<types.Preferences>(config);
      },

    },

    /**
     * Mark your notification read
     */
    read: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OK> => {
      let __path = `/api/v1/notifications/${encodeURIComponent(String(id))}/read`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["OK"],
      };

      return this.request<types.OK>(config);
    },

    /**
     * Retry exhausted notification deliveries for the organization
     */
    retry: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OK> => {
      let __path = `/api/v1/notifications/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["OK"],
      };

      return this.request<types.OK>(config);
    },

    webhooks: {
      /**
       * Create a signed deployment webhook
       */
      create: async (body: types.WebhookEndpointInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.WebhookEndpointSecret> => {
        let __path = `/api/v1/notifications/webhooks`;
        const config: RequestConfig = {
          method: 'POST',
          url: __path,
          body,
          bodyCodec: CODECS["WebhookEndpointInputBody"],
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["WebhookEndpointSecret"],
        };

        return this.request<types.WebhookEndpointSecret>(config);
      },

      /**
       * Delete a webhook endpoint
       */
      delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OK> => {
        let __path = `/api/v1/notifications/webhooks/${encodeURIComponent(String(id))}`;
        const config: RequestConfig = {
          method: 'DELETE',
          url: __path,
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["OK"],
        };

        return this.request<types.OK>(config);
      },

      /**
       * Recent webhook delivery outcomes
       */
      deliveries: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeliveryList> => {
        let __path = `/api/v1/notifications/webhooks/${encodeURIComponent(String(id))}/deliveries`;
        const config: RequestConfig = {
          method: 'GET',
          url: __path,
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["DeliveryList"],
        };

        return this.request<types.DeliveryList>(config);
      },

      /**
       * Organization deployment webhooks
       */
      list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.WebhookEndpointList> => {
        let __path = `/api/v1/notifications/webhooks`;
        const config: RequestConfig = {
          method: 'GET',
          url: __path,
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["WebhookEndpointList"],
        };

        return this.request<types.WebhookEndpointList>(config);
      },

      /**
       * Rotate the signing secret
       */
      rotate: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.WebhookEndpointSecret> => {
        let __path = `/api/v1/notifications/webhooks/${encodeURIComponent(String(id))}/rotate`;
        const config: RequestConfig = {
          method: 'POST',
          url: __path,
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["WebhookEndpointSecret"],
        };

        return this.request<types.WebhookEndpointSecret>(config);
      },

    },

  };

  public readonly octopus = {
    /**
     * An Octopus gateway's routes by domain: the ones added here, the ones FARP found and the ones made in the Octopus admin
     */
    board: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Board> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/octopus/${encodeURIComponent(String(resource))}/board`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Board"],
      };

      return this.request<types.Board>(config);
    },

    /**
     * Add a route to an Octopus gateway and push it, without a redeploy
     */
    createRoute: async (projectId: string, environmentId: string, resource: string, body: types.OctopusRouteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/octopus/${encodeURIComponent(String(resource))}/routes`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["OctopusRouteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteResult"],
      };

      return this.request<types.RouteResult>(config);
    },

    /**
     * The services an Octopus gateway's FARP discovery found or is waiting for
     */
    discovery: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DiscoverySummary> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/octopus/${encodeURIComponent(String(resource))}/discovery`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DiscoverySummary"],
      };

      return this.request<types.DiscoverySummary>(config);
    },

    /**
     * Remove a route at the revision read; a FARP route it hid comes back
     */
    removeRoute: async (projectId: string, environmentId: string, resource: string, routeId: string, body: types.RouteRevisionInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SyncResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/octopus/${encodeURIComponent(String(resource))}/routes/${encodeURIComponent(String(routeId))}/remove`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RouteRevisionInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SyncResult"],
      };

      return this.request<types.SyncResult>(config);
    },

    /**
     * Set the attached domains FARP routes answer on, at the gateway's revision; none is any host
     */
    setFarpHostnames: async (projectId: string, environmentId: string, resource: string, body: types.FarpHostnamesInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OctopusStatus> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/octopus/${encodeURIComponent(String(resource))}/farp-hostnames`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["FarpHostnamesInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["OctopusStatus"],
      };

      return this.request<types.OctopusStatus>(config);
    },

    /**
     * An Octopus gateway's state, its last sync and what attaching a domain to it takes
     */
    status: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OctopusStatus> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/octopus/${encodeURIComponent(String(resource))}/status`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["OctopusStatus"],
      };

      return this.request<types.OctopusStatus>(config);
    },

    /**
     * Push an Octopus gateway's routes now, and register its services with FARP again
     */
    sync: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SyncResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/octopus/${encodeURIComponent(String(resource))}/sync`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SyncResult"],
      };

      return this.request<types.SyncResult>(config);
    },

    /**
     * Change a route at the revision read, and push it
     */
    updateRoute: async (projectId: string, environmentId: string, resource: string, routeId: string, body: types.OctopusRouteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RouteResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/octopus/${encodeURIComponent(String(resource))}/routes/${encodeURIComponent(String(routeId))}/update`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["OctopusRouteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RouteResult"],
      };

      return this.request<types.RouteResult>(config);
    },

  };

  public readonly organizationCloudChanges = {
    /**
     * organizationCloudChanges.apply
     */
    apply: async (body: types.ApplyCloudInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/clouds/changes/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyCloudInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

    /**
     * organizationCloudChanges.getPreview
     */
    getPreview: async (previewId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preview> => {
      let __path = `/api/v1/clouds/changes/${encodeURIComponent(String(previewId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Preview"],
      };

      return this.request<types.Preview>(config);
    },

    /**
     * organizationCloudChanges.preview
     */
    preview: async (body: types.ChangeInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preview> => {
      let __path = `/api/v1/clouds/changes/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ChangeInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Preview"],
      };

      return this.request<types.Preview>(config);
    },

  };

  public readonly organizationCloudSyncs = {
    /**
     * organizationCloudSyncs.applyRevert
     */
    applyRevert: async (body: types.ApplyCloudInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/clouds/syncs/revert/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyCloudInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

    /**
     * organizationCloudSyncs.completeRouting
     */
    completeRouting: async (syncId: string, body: types.CompleteCloudRoutingInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/clouds/syncs/${encodeURIComponent(String(syncId))}/routing`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CompleteCloudRoutingInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

    /**
     * organizationCloudSyncs.get
     */
    get: async (syncId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/clouds/syncs/${encodeURIComponent(String(syncId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

    /**
     * organizationCloudSyncs.list
     */
    list: async (before?: string | undefined, limit?: number | undefined, cloud?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudSyncList> => {
      let __path = `/api/v1/clouds/syncs`;
      const queryParams: Record<string, any> = {};
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (cloud !== undefined) {
        queryParams['cloud'] = cloud;
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
        responseCodec: CODECS["CloudSyncList"],
      };

      return this.request<types.CloudSyncList>(config);
    },

    /**
     * organizationCloudSyncs.previewRevert
     */
    previewRevert: async (syncId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Preview> => {
      let __path = `/api/v1/clouds/syncs/${encodeURIComponent(String(syncId))}/revert/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Preview"],
      };

      return this.request<types.Preview>(config);
    },

    /**
     * organizationCloudSyncs.retry
     */
    retry: async (syncId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sync> => {
      let __path = `/api/v1/clouds/syncs/${encodeURIComponent(String(syncId))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sync"],
      };

      return this.request<types.Sync>(config);
    },

  };

  public readonly organizationCloudTargets = {
    /**
     * organizationCloudTargets.create
     */
    create: async (cloudId: string, body: types.CreateTargetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Target> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/targets`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateTargetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Target"],
      };

      return this.request<types.Target>(config);
    },

    /**
     * organizationCloudTargets.delete
     */
    delete: async (cloudId: string, targetId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
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
     * organizationCloudTargets.get
     */
    get: async (cloudId: string, targetId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Target> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Target"],
      };

      return this.request<types.Target>(config);
    },

    /**
     * organizationCloudTargets.list
     */
    list: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudTargetList> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/targets`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudTargetList"],
      };

      return this.request<types.CloudTargetList>(config);
    },

    /**
     * organizationCloudTargets.update
     */
    update: async (cloudId: string, targetId: string, body: types.UpdateTargetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Target> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateTargetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Target"],
      };

      return this.request<types.Target>(config);
    },

  };

  public readonly organizationClouds = {
    /**
     * organizationClouds.create
     */
    create: async (body: types.CloudprofilesCreateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/clouds`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CloudprofilesCreateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Profile"],
      };

      return this.request<types.Profile>(config);
    },

    /**
     * organizationClouds.delete
     */
    delete: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}`;
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
     * organizationClouds.get
     */
    get: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Profile"],
      };

      return this.request<types.Profile>(config);
    },

    /**
     * organizationClouds.grantProject
     */
    grantProject: async (cloudId: string, projectId: string, body: types.CloudProjectGrantInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProjectGrant> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/projects/${encodeURIComponent(String(projectId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["CloudProjectGrantInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProjectGrant"],
      };

      return this.request<types.ProjectGrant>(config);
    },

    /**
     * organizationClouds.list
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlanningCloudList> => {
      let __path = `/api/v1/clouds`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PlanningCloudList"],
      };

      return this.request<types.PlanningCloudList>(config);
    },

    /**
     * organizationClouds.listGrants
     */
    listGrants: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudProjectGrantList> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/projects`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudProjectGrantList"],
      };

      return this.request<types.CloudProjectGrantList>(config);
    },

    /**
     * organizationClouds.revokeProject
     */
    revokeProject: async (cloudId: string, projectId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProjectGrant> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/projects/${encodeURIComponent(String(projectId))}`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProjectGrant"],
      };

      return this.request<types.ProjectGrant>(config);
    },

    /**
     * organizationClouds.setDefaultPlacement
     */
    setDefaultPlacement: async (cloudId: string, body: types.SetDefaultPlacementInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/default-placement`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["SetDefaultPlacementInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Profile"],
      };

      return this.request<types.Profile>(config);
    },

    /**
     * organizationClouds.update
     */
    update: async (cloudId: string, body: types.UpdateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Profile"],
      };

      return this.request<types.Profile>(config);
    },

    /**
     * organizationClouds.usage
     */
    usage: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Usage> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/usage`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Usage"],
      };

      return this.request<types.Usage>(config);
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
    list: async (id: string, environment?: string | undefined, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlanList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/plans`;
      const queryParams: Record<string, any> = {};
      if (environment !== undefined) {
        queryParams['environment'] = environment;
      }
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
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
     * Exclude services from a project's graph, plans and deploys, by service or path. A project setting: the repository doesn't change.
     */
    exclude: async (id: string, body: types.ExcludeInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Project> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/exclusions`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ExcludeInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Project"],
      };

      return this.request<types.Project>(config);
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
     * Remove exclusions, bringing those services back into the graph and the next plan
     */
    include: async (id: string, body: types.IncludeInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Project> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/exclusions/remove`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["IncludeInputBody"],
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

  public readonly realtime = {
    /**
     */
    events: async (orgId?: string | undefined, projectIds?: string | undefined, deploymentIds?: string | undefined, cursor?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/realtime/events`;
      const queryParams: Record<string, any> = {};
      if (orgId !== undefined) {
        queryParams['org_id'] = orgId;
      }
      if (projectIds !== undefined) {
        queryParams['project_ids'] = projectIds;
      }
      if (deploymentIds !== undefined) {
        queryParams['deployment_ids'] = deploymentIds;
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
        allowEmptyBody: true,
      };

      await this.request(config);
    },

    /**
     */
    snapshot: async (orgId?: string | undefined, projectIds?: string | undefined, deploymentIds?: string | undefined, cursor?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Snapshot> => {
      let __path = `/api/v1/realtime/snapshot`;
      const queryParams: Record<string, any> = {};
      if (orgId !== undefined) {
        queryParams['org_id'] = orgId;
      }
      if (projectIds !== undefined) {
        queryParams['project_ids'] = projectIds;
      }
      if (deploymentIds !== undefined) {
        queryParams['deployment_ids'] = deploymentIds;
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
        responseCodec: CODECS["Snapshot"],
      };

      return this.request<types.Snapshot>(config);
    },

    /**
     */
    ws: async (orgId?: string | undefined, projectIds?: string | undefined, deploymentIds?: string | undefined, cursor?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/realtime/ws`;
      const queryParams: Record<string, any> = {};
      if (orgId !== undefined) {
        queryParams['org_id'] = orgId;
      }
      if (projectIds !== undefined) {
        queryParams['project_ids'] = projectIds;
      }
      if (deploymentIds !== undefined) {
        queryParams['deployment_ids'] = deploymentIds;
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
        allowEmptyBody: true,
      };

      await this.request(config);
    },

  };

  public readonly retentionPolicies = {
    /**
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetentionPolicyState> => {
      let __path = `/api/v1/service-backups/${encodeURIComponent(String(id))}/retention-policy`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RetentionPolicyState"],
      };

      return this.request<types.RetentionPolicyState>(config);
    },

    /**
     */
    history: async (id: string, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetentionPolicyHistory> => {
      let __path = `/api/v1/service-backups/${encodeURIComponent(String(id))}/retention-policy/history`;
      const queryParams: Record<string, any> = {};
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
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
        responseCodec: CODECS["RetentionPolicyHistory"],
      };

      return this.request<types.RetentionPolicyHistory>(config);
    },

    /**
     */
    set: async (id: string, body: types.RetentionPolicyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetentionPolicy> => {
      let __path = `/api/v1/service-backups/${encodeURIComponent(String(id))}/retention-policy`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["RetentionPolicyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RetentionPolicy"],
      };

      return this.request<types.RetentionPolicy>(config);
    },

  };

  public readonly serviceBackups = {
    /**
     */
    create: async (id: string, body: types.BackupCreateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Backup> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/service-backups`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["BackupCreateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Backup"],
      };

      return this.request<types.Backup>(config);
    },

    /**
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Backup> => {
      let __path = `/api/v1/service-backups/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Backup"],
      };

      return this.request<types.Backup>(config);
    },

    /**
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.BackupList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/service-backups`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["BackupList"],
      };

      return this.request<types.BackupList>(config);
    },

    /**
     */
    previewRetention: async (id: string, olderThanDays?: number, keepLast?: number, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetentionPreview> => {
      let __path = `/api/v1/service-backups/${encodeURIComponent(String(id))}/retention-preview`;
      const queryParams: Record<string, any> = {};
      queryParams['older_than_days'] = olderThanDays;
      queryParams['keep_last'] = keepLast;
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RetentionPreview"],
      };

      return this.request<types.RetentionPreview>(config);
    },

    /**
     */
    retry: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Backup> => {
      let __path = `/api/v1/service-backups/${encodeURIComponent(String(id))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Backup"],
      };

      return this.request<types.Backup>(config);
    },

  };

  public readonly serviceRestores = {
    /**
     */
    create: async (id: string, body: types.RestoreInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Restore> => {
      let __path = `/api/v1/service-backups/${encodeURIComponent(String(id))}/restore`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RestoreInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Restore"],
      };

      return this.request<types.Restore>(config);
    },

    /**
     */
    delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/service-restores/${encodeURIComponent(String(id))}`;
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
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Restore> => {
      let __path = `/api/v1/service-restores/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Restore"],
      };

      return this.request<types.Restore>(config);
    },

    /**
     */
    list: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RestoreList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/service-restores`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RestoreList"],
      };

      return this.request<types.RestoreList>(config);
    },

  };

  public readonly serviceRetirements = {
    /**
     */
    create: async (id: string, body: types.RetirementInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Retirement> => {
      let __path = `/api/v1/service-backups/${encodeURIComponent(String(id))}/retire`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RetirementInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Retirement"],
      };

      return this.request<types.Retirement>(config);
    },

    /**
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Retirement> => {
      let __path = `/api/v1/service-backup-retirements/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Retirement"],
      };

      return this.request<types.Retirement>(config);
    },

    /**
     */
    history: async (id: string, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetirementHistory> => {
      let __path = `/api/v1/service-backup-retirements/${encodeURIComponent(String(id))}/history`;
      const queryParams: Record<string, any> = {};
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
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
        responseCodec: CODECS["RetirementHistory"],
      };

      return this.request<types.RetirementHistory>(config);
    },

    /**
     */
    list: async (id: string, backupId?: string | undefined, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RetirementPage> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/service-backup-retirements`;
      const queryParams: Record<string, any> = {};
      if (backupId !== undefined) {
        queryParams['backup_id'] = backupId;
      }
      if (before !== undefined) {
        queryParams['before'] = before;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
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
        responseCodec: CODECS["RetirementPage"],
      };

      return this.request<types.RetirementPage>(config);
    },

    /**
     */
    retry: async (id: string, body: types.RetirementRetryInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Retirement> => {
      let __path = `/api/v1/service-backup-retirements/${encodeURIComponent(String(id))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RetirementRetryInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Retirement"],
      };

      return this.request<types.Retirement>(config);
    },

  };

  public readonly session = {
    /**
     * The signed-in user, their active organization and their permissions in it
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.IdentitySession> => {
      let __path = `/api/v1/session`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["IdentitySession"],
      };

      return this.request<types.IdentitySession>(config);
    },

  };

  public readonly sources = {
    /**
     * Attach a repository, or a directory of one, after checking Loomplane can read it
     */
    create: async (id: string, body: types.AddSourceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DiscoverySource> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/sources`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["AddSourceInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DiscoverySource"],
      };

      return this.request<types.DiscoverySource>(config);
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
    update: async (id: string, alias: string, body: types.UpdateSourceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DiscoverySource> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/sources/${encodeURIComponent(String(alias))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateSourceInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DiscoverySource"],
      };

      return this.request<types.DiscoverySource>(config);
    },

  };

  public readonly usage = {
    /**
     * What each connected account has cost this month, read from the cloud's own billing, and why any account can't say
     */
    spend: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SpendReport> => {
      let __path = `/api/v1/usage/spend`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SpendReport"],
      };

      return this.request<types.SpendReport>(config);
    },

    /**
     * Requests and latency of every service you can see, read from each cloud's own monitoring, beside the stretch before
     */
    traffic: async (range?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.TrafficReport> => {
      let __path = `/api/v1/usage/traffic`;
      const queryParams: Record<string, any> = {};
      if (range !== undefined) {
        queryParams['range'] = range;
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
        responseCodec: CODECS["TrafficReport"],
      };

      return this.request<types.TrafficReport>(config);
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
