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

  public readonly agentConversations = {
    /**
     * Delete your conversation
     * Only the person who started the conversation deletes it. It leaves your list, reads as not found and takes no more messages. When the agent is still on its last message, that task is stopped first. Its tasks, their events and requests are kept.
     */
    delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/agent/conversations/${encodeURIComponent(String(id))}`;
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
     * A page of a conversation's turns, each with its run's events
     * The person who started it and those who manage the workspace's AI may read it. Stream a turn that's still running with after set to its event_seq.
     */
    get: async (id: string, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.History> => {
      let __path = `/api/v1/agent/conversations/${encodeURIComponent(String(id))}`;
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
        responseCodec: CODECS["History"],
      };

      return this.request<types.History>(config);
    },

    /**
     * Your conversations with the agent, the most recently updated first
     * With everyone=true, everyone's in the workspace, which needs ai:manage. For the next page, pass the last conversation's updated_at as before.
     */
    list: async (projectId?: string | undefined, everyone?: boolean | undefined, before?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ConversationList> => {
      let __path = `/api/v1/agent/conversations`;
      const queryParams: Record<string, any> = {};
      if (projectId !== undefined) {
        queryParams['project_id'] = projectId;
      }
      if (everyone !== undefined) {
        queryParams['everyone'] = everyone;
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
        responseCodec: CODECS["ConversationList"],
      };

      return this.request<types.ConversationList>(config);
    },

    /**
     * Add a message to your conversation
     * Only the person who started the conversation adds to it, once the agent has finished the last message. context, when given, says where in the dashboard you sent it from.
     */
    send: async (id: string, body: types.SendInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sent> => {
      let __path = `/api/v1/agent/conversations/${encodeURIComponent(String(id))}/messages`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["SendInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sent"],
      };

      return this.request<types.Sent>(config);
    },

    /**
     * Start a conversation with the agent
     * The message starts the conversation's first task. Follow it on GET /agent/tasks/{id}/stream. context, when given, says where in the dashboard you sent it from: the agent reads that page first.
     */
    start: async (body: types.StartInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Sent> => {
      let __path = `/api/v1/agent/conversations`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["StartInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Sent"],
      };

      return this.request<types.Sent>(config);
    },

    /**
     * Rename, pin or unpin your conversation
     * Only the person who started the conversation changes it. A title you give is 1 to 120 characters and stays: no automatic title replaces it. Leave out what you don't change.
     */
    update: async (id: string, body: types.ConversationUpdateBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Conversation> => {
      let __path = `/api/v1/agent/conversations/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["ConversationUpdateBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Conversation"],
      };

      return this.request<types.Conversation>(config);
    },

  };

  public readonly agentReports = {
    /**
     * Close a report
     * Needs deployment:create on the report's project. If the report has a GitHub issue, Loomplane comments there and closes it.
     */
    close: async (id: string, body: types.CloseInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Report> => {
      let __path = `/api/v1/agent/reports/${encodeURIComponent(String(id))}/close`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CloseInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Report"],
      };

      return this.request<types.Report>(config);
    },

    /**
     * One report the agent opened
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Report> => {
      let __path = `/api/v1/agent/reports/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Report"],
      };

      return this.request<types.Report>(config);
    },

    /**
     * What the agent saw go wrong in your workspace
     * Only the reports of projects whose deployments you may read.
     */
    list: async (status?: string | undefined, projectId?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ReportList> => {
      let __path = `/api/v1/agent/reports`;
      const queryParams: Record<string, any> = {};
      if (status !== undefined) {
        queryParams['status'] = status;
      }
      if (projectId !== undefined) {
        queryParams['project_id'] = projectId;
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
        responseCodec: CODECS["ReportList"],
      };

      return this.request<types.ReportList>(config);
    },

  };

  public readonly agentRequests = {
    /**
     * Answer the agent's question
     * The person asked, a member of the team it was sent to, or someone who manages the workspace's AI may answer. The first answer counts.
     */
    answer: async (id: string, body: types.AnswerInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Request> => {
      let __path = `/api/v1/agent/requests/${encodeURIComponent(String(id))}/answer`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["AnswerInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Request"],
      };

      return this.request<types.Request>(config);
    },

    /**
     * Say the GitHub or cloud connection the agent asked for is made
     * Loomplane checks the connection exists before the agent goes on.
     */
    complete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Request> => {
      let __path = `/api/v1/agent/requests/${encodeURIComponent(String(id))}/complete`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Request"],
      };

      return this.request<types.Request>(config);
    },

    /**
     * Decline the agent's request
     * Declining a plan approval rejects the plan.
     */
    decline: async (id: string, body: types.DeclineInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Request> => {
      let __path = `/api/v1/agent/requests/${encodeURIComponent(String(id))}/decline`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["DeclineInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Request"],
      };

      return this.request<types.Request>(config);
    },

    /**
     * One request the agent made
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Request> => {
      let __path = `/api/v1/agent/requests/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Request"],
      };

      return this.request<types.Request>(config);
    },

    /**
     * What the agent is asking of people in your workspace
     */
    list: async (status?: string | undefined, projectId?: string | undefined, forParam?: string | undefined, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RequestList> => {
      let __path = `/api/v1/agent/requests`;
      const queryParams: Record<string, any> = {};
      if (status !== undefined) {
        queryParams['status'] = status;
      }
      if (projectId !== undefined) {
        queryParams['project_id'] = projectId;
      }
      if (forParam !== undefined) {
        queryParams['for'] = forParam;
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
        responseCodec: CODECS["RequestList"],
      };

      return this.request<types.RequestList>(config);
    },

    /**
     * Set the secret the agent asked for
     * Needs variable:manage on the request's project. The value is stored as a secret variable, as you; the agent only hears that it's set.
     */
    provideSecret: async (id: string, body: types.SecretInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Request> => {
      let __path = `/api/v1/agent/requests/${encodeURIComponent(String(id))}/secret`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["SecretInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Request"],
      };

      return this.request<types.Request>(config);
    },

  };

  public readonly agentStatus = {
    /**
     * Whether the AI agent is on, on this server and in the workspace
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.AgentsettingsStatus> => {
      let __path = `/api/v1/agent/status`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["AgentsettingsStatus"],
      };

      return this.request<types.AgentsettingsStatus>(config);
    },

  };

  public readonly agentTasks = {
    /**
     * Stop one of the agent's tasks
     * The person who asked for it, or someone who manages the workspace's AI, may stop it.
     */
    cancel: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Task> => {
      let __path = `/api/v1/agent/tasks/${encodeURIComponent(String(id))}/cancel`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Task"],
      };

      return this.request<types.Task>(config);
    },

    /**
     * One of the agent's tasks
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Task> => {
      let __path = `/api/v1/agent/tasks/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Task"],
      };

      return this.request<types.Task>(config);
    },

    /**
     * Follow a task's events as they happen
     * Each agent.event carries one event, with its seq as the SSE id. Reconnect with Last-Event-ID (or after) set to the last seq you have, and the stream picks up at the next one. agent.end is the last frame: the task's final status.
     */
    stream: async (id: string, after?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/agent/tasks/${encodeURIComponent(String(id))}/stream`;
      const queryParams: Record<string, any> = {};
      if (after !== undefined) {
        queryParams['after'] = after;
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

  public readonly agentWatcher = {
    /**
     * Whether the agent is watching your workspace, and where its reports go on GitHub
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.AgentwatchStatus> => {
      let __path = `/api/v1/agent/watcher`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["AgentwatchStatus"],
      };

      return this.request<types.AgentwatchStatus>(config);
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
     * Delete a provider key, unless the agent or the workspace embeddings use it
     */
    deleteProviderKey: async (name: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/ai/settings/provider-keys/${encodeURIComponent(String(name))}`;
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
     * The agent's settings, what it runs with and its month's budget, without any key
     */
    getAgent: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.AgentView> => {
      let __path = `/api/v1/ai/settings/agent`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["AgentView"],
      };

      return this.request<types.AgentView>(config);
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

    /**
     * Add a provider key, or change one; its secrets are never shown again
     * Anthropic, OpenAI and OpenAI-compatible keys only. Secret fields are write-only: no answer carries them, and only the key's hint (its last four characters) is shown. A secret field left out keeps its value. The key the agent uses must stay granted to every project.
     */
    putProviderKey: async (name: string, body: types.ProviderKeyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProviderKey> => {
      let __path = `/api/v1/ai/settings/provider-keys/${encodeURIComponent(String(name))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["ProviderKeyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProviderKey"],
      };

      return this.request<types.ProviderKey>(config);
    },

    /**
     * Set the agent's monthly budget, its share of the workspace's; 0 releases it while the agent is off
     */
    setAgentBudget: async (body: types.AgentBudgetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.AgentView> => {
      let __path = `/api/v1/ai/settings/agent/budget`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["AgentBudgetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["AgentView"],
      };

      return this.request<types.AgentView>(config);
    },

    /**
     * Set the workspace's monthly AI budget, which every allocation shares
     */
    setBudget: async (body: types.WorkspaceBudgetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.View> => {
      let __path = `/api/v1/ai/settings/budget`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["WorkspaceBudgetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["View"],
      };

      return this.request<types.View>(config);
    },

    /**
     * Turn the agent on or off, or change its provider key or model
     * Turning the agent on needs its monthly budget, a provider key (or the server's key) and a model this server prices: nexus's catalog or ai.prices. A key's own model prices don't count for the agent.
     */
    updateAgent: async (body: types.AgentUpdateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.AgentView> => {
      let __path = `/api/v1/ai/settings/agent`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["AgentUpdateInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["AgentView"],
      };

      return this.request<types.AgentView>(config);
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
     * Revoke an API key, at once. With compromised=true, also list the nodes that joined with a token it made.
     */
    revoke: async (id: string, compromised?: boolean | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CompromisedRevoke | void> => {
      let __path = `/api/v1/api-keys/${encodeURIComponent(String(id))}`;
      const queryParams: Record<string, any> = {};
      if (compromised !== undefined) {
        queryParams['compromised'] = compromised;
      }
      const queryString = new URLSearchParams(queryParams).toString();
      if (queryString) {
        __path += '?' + queryString;
      }
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        allowEmptyBody: true,
        responseCodec: CODECS["CompromisedRevoke"],
      };

      return this.request<types.CompromisedRevoke | void>(config);
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

    /**
     * Name what a binding runs; the alias changes no cloud resource, and empty clears it
     */
    setAlias: async (id: string, binding: string, body: types.BindingAliasInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Binding> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/bindings/${encodeURIComponent(String(binding))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["BindingAliasInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Binding"],
      };

      return this.request<types.Binding>(config);
    },

  };

  public readonly bitbucket = {
    /**
     * The Bitbucket link that starts connecting a workspace
     */
    authorize: async (body: types.AuthorizeBitbucketInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.BitbucketAuthorization> => {
      let __path = `/api/v1/bitbucket/authorize`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["AuthorizeBitbucketInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["BitbucketAuthorization"],
      };

      return this.request<types.BitbucketAuthorization>(config);
    },

    /**
     * Connect a Bitbucket workspace, from what Bitbucket's redirect carried back
     */
    connect: async (body: types.BitbucketConnectInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.BitbucketWorkspaceView> => {
      let __path = `/api/v1/bitbucket/workspaces`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["BitbucketConnectInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["BitbucketWorkspaceView"],
      };

      return this.request<types.BitbucketWorkspaceView>(config);
    },

    /**
     * Disconnect a workspace. The consumer stays approved on Bitbucket.
     */
    disconnect: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/bitbucket/workspaces/${encodeURIComponent(String(id))}`;
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
     * The repositories the organization's workspaces can read
     */
    repositories: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.BitbucketRepositoryList> => {
      let __path = `/api/v1/bitbucket/repositories`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["BitbucketRepositoryList"],
      };

      return this.request<types.BitbucketRepositoryList>(config);
    },

    /**
     * The organization's Bitbucket workspaces, and whether this Loomplane has an OAuth consumer
     */
    status: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.BitbucketStatus> => {
      let __path = `/api/v1/bitbucket`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["BitbucketStatus"],
      };

      return this.request<types.BitbucketStatus>(config);
    },

    webhook: {
      /**
       * Try again to add the push webhook that re-analyzes a repository's sources
       */
      enable: async (body: types.EnableBitbucketWebhookInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PushState> => {
        let __path = `/api/v1/bitbucket/webhooks`;
        const config: RequestConfig = {
          method: 'POST',
          url: __path,
          body,
          bodyCodec: CODECS["EnableBitbucketWebhookInputBody"],
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["PushState"],
        };

        return this.request<types.PushState>(config);
      },

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

  public readonly cli = {
    organization: {
      /**
       * Make one of your organizations the one this session acts in
       */
      use: async (body: types.UseOrganizationInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CLIOrganizationList> => {
        let __path = `/api/v1/cli/organization`;
        const config: RequestConfig = {
          method: 'PUT',
          url: __path,
          body,
          bodyCodec: CODECS["UseOrganizationInputBody"],
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["CLIOrganizationList"],
        };

        return this.request<types.CLIOrganizationList>(config);
      },

    },

    organizations: {
      /**
       * The organizations you belong to, and the one this session acts in
       */
      list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CLIOrganizationList> => {
        let __path = `/api/v1/cli/organizations`;
        const config: RequestConfig = {
          method: 'GET',
          url: __path,
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["CLIOrganizationList"],
        };

        return this.request<types.CLIOrganizationList>(config);
      },

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

  public readonly cloudClusters = {
    /**
     * cloudClusters.attachments
     */
    attachments: async (cloudId: string, clusterId: string, cursor?: string | undefined, limit?: number | undefined, region?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RuntimeAttachmentPage> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/clusters/${encodeURIComponent(String(clusterId))}/attachments`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (region !== undefined) {
        queryParams['region'] = region;
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
        responseCodec: CODECS["RuntimeAttachmentPage"],
      };

      return this.request<types.RuntimeAttachmentPage>(config);
    },

    /**
     * cloudClusters.capacity
     */
    capacity: async (cloudId: string, clusterId: string, cursor?: string | undefined, limit?: number | undefined, region?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ClusterCapacity> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/clusters/${encodeURIComponent(String(clusterId))}/capacity`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (region !== undefined) {
        queryParams['region'] = region;
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
        responseCodec: CODECS["ClusterCapacity"],
      };

      return this.request<types.ClusterCapacity>(config);
    },

    /**
     * cloudClusters.get
     */
    get: async (cloudId: string, clusterId: string, cursor?: string | undefined, limit?: number | undefined, region?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudCluster> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/clusters/${encodeURIComponent(String(clusterId))}`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (region !== undefined) {
        queryParams['region'] = region;
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
        responseCodec: CODECS["CloudCluster"],
      };

      return this.request<types.CloudCluster>(config);
    },

    /**
     * cloudClusters.list
     */
    list: async (cloudId: string, cursor?: string | undefined, limit?: number | undefined, region?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudClusterPage> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/clusters`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (region !== undefined) {
        queryParams['region'] = region;
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
        responseCodec: CODECS["CloudClusterPage"],
      };

      return this.request<types.CloudClusterPage>(config);
    },

    /**
     * cloudClusters.options
     */
    options: async (cloudId: string, connectionId: string, cursor?: string | undefined, limit?: number | undefined, region?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ClusterOptions> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/cluster-options/${encodeURIComponent(String(connectionId))}`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (region !== undefined) {
        queryParams['region'] = region;
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
        responseCodec: CODECS["ClusterOptions"],
      };

      return this.request<types.ClusterOptions>(config);
    },

    /**
     * cloudClusters.preview
     */
    preview: async (cloudId: string, body: types.ClusterInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudClusterOperation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/clusters/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ClusterInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudClusterOperation"],
      };

      return this.request<types.CloudClusterOperation>(config);
    },

    /**
     * cloudClusters.refreshCapacity
     */
    refreshCapacity: async (cloudId: string, clusterId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ClusterCapacity> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/clusters/${encodeURIComponent(String(clusterId))}/capacity/refresh`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ClusterCapacity"],
      };

      return this.request<types.ClusterCapacity>(config);
    },

    /**
     * cloudClusters.retirementPreview
     */
    retirementPreview: async (cloudId: string, clusterId: string, body: types.ClusterRetirementPreviewInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ClusterRetirementReview> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/clusters/${encodeURIComponent(String(clusterId))}/retirement/preview`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ClusterRetirementPreviewInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ClusterRetirementReview"],
      };

      return this.request<types.ClusterRetirementReview>(config);
    },

  };

  public readonly cloudLocationPolicy = {
    /**
     * cloudLocationPolicy.get
     */
    get: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.LocationPolicy> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/location-policy`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["LocationPolicy"],
      };

      return this.request<types.LocationPolicy>(config);
    },

    /**
     * cloudLocationPolicy.save
     */
    save: async (cloudId: string, body: types.LocationPolicyBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.LocationPolicy> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/location-policy`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["LocationPolicyBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["LocationPolicy"],
      };

      return this.request<types.LocationPolicy>(config);
    },

  };

  public readonly cloudLocations = {
    /**
     * cloudLocations.delete
     */
    delete: async (cloudId: string, locationId: string, body: types.CloudLocationDeleteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/locations/${encodeURIComponent(String(locationId))}`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        body,
        bodyCodec: CODECS["CloudLocationDeleteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        allowEmptyBody: true,
      };

      await this.request(config);
    },

    /**
     * cloudLocations.get
     */
    get: async (cloudId: string, locationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudLocation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/locations/${encodeURIComponent(String(locationId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudLocation"],
      };

      return this.request<types.CloudLocation>(config);
    },

    /**
     * cloudLocations.list
     */
    list: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudLocationList> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/locations`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudLocationList"],
      };

      return this.request<types.CloudLocationList>(config);
    },

    /**
     * cloudLocations.save
     */
    save: async (cloudId: string, body: types.LocationInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudLocation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/locations`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["LocationInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudLocation"],
      };

      return this.request<types.CloudLocation>(config);
    },

    /**
     * cloudLocations.verify
     */
    verify: async (cloudId: string, locationId: string, body: types.LocationVerificationInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudLocation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/locations/${encodeURIComponent(String(locationId))}/verification`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["LocationVerificationInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudLocation"],
      };

      return this.request<types.CloudLocation>(config);
    },

  };

  public readonly cloudNodeAdoptions = {
    /**
     * cloudNodeAdoptions.list
     */
    list: async (cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NodeAdoptionList> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/node-adoptions`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NodeAdoptionList"],
      };

      return this.request<types.NodeAdoptionList>(config);
    },

  };

  public readonly cloudPlacements = {
    /**
     * cloudPlacements.acceptMove
     */
    acceptMove: async (cloudId: string, body: types.CloudinfraMoveInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.MoveReview> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/placements/accept-move`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CloudinfraMoveInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["MoveReview"],
      };

      return this.request<types.MoveReview>(config);
    },

    /**
     * cloudPlacements.previewMove
     */
    previewMove: async (cloudId: string, body: types.CloudinfraMoveInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.MoveReview> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/placements/preview-move`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CloudinfraMoveInputBody"],
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
    create: async (id: string, cloudId: string, body: types.CreateTargetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudprofilesTarget> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/targets`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateTargetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudprofilesTarget"],
      };

      return this.request<types.CloudprofilesTarget>(config);
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
    get: async (id: string, cloudId: string, targetId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudprofilesTarget> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudprofilesTarget"],
      };

      return this.request<types.CloudprofilesTarget>(config);
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
    update: async (id: string, cloudId: string, targetId: string, body: types.UpdateTargetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudprofilesTarget> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateTargetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudprofilesTarget"],
      };

      return this.request<types.CloudprofilesTarget>(config);
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
    update: async (id: string, cloudId: string, body: types.CloudprofilesUpdateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["CloudprofilesUpdateInputBody"],
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

  public readonly clusterOperations = {
    /**
     * clusterOperations.apply
     */
    apply: async (cloudId: string, operationId: string, body: types.ApplyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudClusterOperation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/cluster-operations/${encodeURIComponent(String(operationId))}/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudClusterOperation"],
      };

      return this.request<types.CloudClusterOperation>(config);
    },

    /**
     * clusterOperations.cancel
     */
    cancel: async (cloudId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudClusterOperation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/cluster-operations/${encodeURIComponent(String(operationId))}/cancel`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudClusterOperation"],
      };

      return this.request<types.CloudClusterOperation>(config);
    },

    /**
     * clusterOperations.get
     */
    get: async (cloudId: string, operationId: string, cursor?: string | undefined, limit?: number | undefined, region?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudClusterOperation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/cluster-operations/${encodeURIComponent(String(operationId))}`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (region !== undefined) {
        queryParams['region'] = region;
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
        responseCodec: CODECS["CloudClusterOperation"],
      };

      return this.request<types.CloudClusterOperation>(config);
    },

    /**
     * clusterOperations.list
     */
    list: async (cloudId: string, cursor?: string | undefined, limit?: number | undefined, region?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudClusterOperationPage> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/cluster-operations`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (region !== undefined) {
        queryParams['region'] = region;
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
        responseCodec: CODECS["CloudClusterOperationPage"],
      };

      return this.request<types.CloudClusterOperationPage>(config);
    },

    /**
     * clusterOperations.retry
     */
    retry: async (cloudId: string, operationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudClusterOperation> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/cluster-operations/${encodeURIComponent(String(operationId))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudClusterOperation"],
      };

      return this.request<types.CloudClusterOperation>(config);
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

  public readonly connectionClusters = {
    /**
     */
    list: async (connectionId: string, cursor?: string | undefined, limit?: number | undefined, region?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ClusterPage> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(connectionId))}/clusters`;
      const queryParams: Record<string, any> = {};
      if (cursor !== undefined) {
        queryParams['cursor'] = cursor;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (region !== undefined) {
        queryParams['region'] = region;
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
        responseCodec: CODECS["ClusterPage"],
      };

      return this.request<types.ClusterPage>(config);
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
     * Cancel a running deployment before its next step, or a paused one at once; steps that finished stay as they are
     */
    cancel: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeploymentsDeployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/cancel`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeploymentsDeployment"],
      };

      return this.request<types.DeploymentsDeployment>(config);
    },

    /**
     * Cancel a deployment paused waiting for capacity; steps that finished stay as they are
     */
    cancelCapacity: async (id: string, body: types.CapacityWaitInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeploymentsDeployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/capacity/cancel`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CapacityWaitInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeploymentsDeployment"],
      };

      return this.request<types.DeploymentsDeployment>(config);
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
    cleanupCancel: async (id: string, body: types.FailedCleanupCancelInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.FailedCleanup> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/cleanup/cancel`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["FailedCleanupCancelInputBody"],
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
    forPlan: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeploymentsDeployment> => {
      let __path = `/api/v1/plans/${encodeURIComponent(String(id))}/deployment`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeploymentsDeployment"],
      };

      return this.request<types.DeploymentsDeployment>(config);
    },

    /**
     * One deployment, with each step's progress
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeploymentsDeployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeploymentsDeployment"],
      };

      return this.request<types.DeploymentsDeployment>(config);
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
    resumeCapacity: async (id: string, body: types.CapacityWaitInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeploymentsDeployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/capacity/resume`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CapacityWaitInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeploymentsDeployment"],
      };

      return this.request<types.DeploymentsDeployment>(config);
    },

    /**
     * Run a failed deployment's failed and skipped steps again, with the plan that was approved
     */
    retry: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DeploymentsDeployment> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["DeploymentsDeployment"],
      };

      return this.request<types.DeploymentsDeployment>(config);
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

  public readonly domainEdge = {
    /**
     * domainEdge.get
     */
    get: async (projectId: string, routeId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EdgeState> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/edge`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EdgeState"],
      };

      return this.request<types.EdgeState>(config);
    },

    /**
     * domainEdge.purge
     */
    purge: async (projectId: string, routeId: string, body: types.EdgePurgeInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EdgeState> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/edge/purge`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["EdgePurgeInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EdgeState"],
      };

      return this.request<types.EdgeState>(config);
    },

    /**
     * domainEdge.retry
     */
    retry: async (projectId: string, routeId: string, body: types.RemoveRouteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EdgeState> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/edge/retry`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RemoveRouteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EdgeState"],
      };

      return this.request<types.EdgeState>(config);
    },

    /**
     * domainEdge.update
     */
    update: async (projectId: string, routeId: string, body: types.EdgeInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.EdgeState> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/domain-routes/${encodeURIComponent(String(routeId))}/edge`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["EdgeInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["EdgeState"],
      };

      return this.request<types.EdgeState>(config);
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
    status: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.FabriqloomStatus> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["FabriqloomStatus"],
      };

      return this.request<types.FabriqloomStatus>(config);
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
    create: async (projectId: string, environmentId: string, resource: string, body: types.PlanningTenantInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.FabriqloomTenant> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/tenants`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["PlanningTenantInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["FabriqloomTenant"],
      };

      return this.request<types.FabriqloomTenant>(config);
    },

    /**
     * A hosted Fabriq's tenants, by id
     */
    list: async (projectId: string, environmentId: string, resource: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlanningTenantList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/environments/${encodeURIComponent(String(environmentId))}/fabriq/${encodeURIComponent(String(resource))}/tenants`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PlanningTenantList"],
      };

      return this.request<types.PlanningTenantList>(config);
    },

  };

  public readonly fleetInstances = {
    /**
     * Request a new instance
     */
    create: async (projectId: string, body: types.CreateInstanceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.OperationResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/fleet/instances`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateInstanceInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["OperationResult"],
      };

      return this.request<types.OperationResult>(config);
    },

    /**
     * Read an instance
     */
    get: async (projectId: string, id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Instance> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/fleet/instances/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Instance"],
      };

      return this.request<types.Instance>(config);
    },

    /**
     * List instances in this fleet
     */
    list: async (projectId: string, after?: string | undefined, limit?: number | undefined, status?: string | undefined, externalRef?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.InstancePage> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/fleet/instances`;
      const queryParams: Record<string, any> = {};
      if (after !== undefined) {
        queryParams['after'] = after;
      }
      if (limit !== undefined) {
        queryParams['limit'] = limit;
      }
      if (status !== undefined) {
        queryParams['status'] = status;
      }
      if (externalRef !== undefined) {
        queryParams['external_ref'] = externalRef;
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
        responseCodec: CODECS["InstancePage"],
      };

      return this.request<types.InstancePage>(config);
    },

  };

  public readonly fleetOperations = {
    /**
     * Read an instance operation
     */
    get: async (projectId: string, id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.FleetOperation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/fleet/operations/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["FleetOperation"],
      };

      return this.request<types.FleetOperation>(config);
    },

  };

  public readonly fleetReleases = {
    /**
     * Read an immutable fleet release
     */
    get: async (projectId: string, id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.TemplateRelease> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/fleet/releases/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["TemplateRelease"],
      };

      return this.request<types.TemplateRelease>(config);
    },

  };

  public readonly fleets = {
    /**
     * Read the project's instance fleet
     */
    get: async (projectId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Fleet> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(projectId))}/fleet`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Fleet"],
      };

      return this.request<types.Fleet>(config);
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

  public readonly gitlab = {
    /**
     * Register a GitLab of the organization's own, with the OAuth application made on it
     */
    addHost: async (body: types.AddGitLabHostInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GitLabHostView> => {
      let __path = `/api/v1/gitlab/hosts`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["AddGitLabHostInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GitLabHostView"],
      };

      return this.request<types.GitLabHostView>(config);
    },

    /**
     * Connect a GitLab account, from what GitLab's redirect carried back
     */
    connect: async (body: types.ConnectGitLabInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GitLabConnectionView> => {
      let __path = `/api/v1/gitlab/connections`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ConnectGitLabInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GitLabConnectionView"],
      };

      return this.request<types.GitLabConnectionView>(config);
    },

    /**
     * Disconnect an account, and revoke its sign-in at GitLab
     */
    disconnect: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/gitlab/connections/${encodeURIComponent(String(id))}`;
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
     * Remove a host the organization registered. Its accounts have to be disconnected first.
     */
    removeHost: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/gitlab/hosts/${encodeURIComponent(String(id))}`;
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
     * The projects the organization's connected accounts can read
     */
    repositories: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GitLabRepositoryList> => {
      let __path = `/api/v1/gitlab/repositories`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GitLabRepositoryList"],
      };

      return this.request<types.GitLabRepositoryList>(config);
    },

    /**
     * Start signing in to a GitLab host, and get the address to send the browser to
     */
    startSignIn: async (body: types.StartGitLabSignInInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GitLabSignInStart> => {
      let __path = `/api/v1/gitlab/sign-ins`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["StartGitLabSignInInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GitLabSignInStart"],
      };

      return this.request<types.GitLabSignInStart>(config);
    },

    /**
     * The organization's GitLab hosts and connected accounts
     */
    status: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GitLabStatus> => {
      let __path = `/api/v1/gitlab`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GitLabStatus"],
      };

      return this.request<types.GitLabStatus>(config);
    },

    /**
     * Create or replace a connection's webhook secret, shown once
     */
    webhookSecret: async (id: string, body: types.SetGitLabWebhookInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GitLabWebhookSecret> => {
      let __path = `/api/v1/gitlab/connections/${encodeURIComponent(String(id))}/webhook`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["SetGitLabWebhookInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GitLabWebhookSecret"],
      };

      return this.request<types.GitLabWebhookSecret>(config);
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

  public readonly nodeJoinTokens = {
    /**
     * Cancel a join token no machine has used, so none can
     */
    cancel: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/nodes/join-tokens/${encodeURIComponent(String(id))}`;
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
     * Make a single-use token for one machine to join, and the command that uses it
     */
    create: async (body: types.JoinTokenRequestBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.JoinToken> => {
      let __path = `/api/v1/nodes/join-tokens`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["JoinTokenRequestBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["JoinToken"],
      };

      return this.request<types.JoinToken>(config);
    },

    /**
     * The join tokens no machine has used yet: who made them, with which key, for what, and when they expire
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.JoinTokenList> => {
      let __path = `/api/v1/nodes/join-tokens`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["JoinTokenList"],
      };

      return this.request<types.JoinTokenList>(config);
    },

  };

  public readonly nodes = {
    /**
     * Review draining the Cloud server a node was adopted as: what it holds now
     */
    drain: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.DrainReview> => {
      let __path = `/api/v1/nodes/${encodeURIComponent(String(id))}/drain`;
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
     * Drain a node's server as reviewed, so nothing new is placed on it
     */
    drainApply: async (id: string, body: types.ApplyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Node> => {
      let __path = `/api/v1/nodes/${encodeURIComponent(String(id))}/drain/apply`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["ApplyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Node"],
      };

      return this.request<types.Node>(config);
    },

    /**
     * One node
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Node> => {
      let __path = `/api/v1/nodes/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Node"],
      };

      return this.request<types.Node>(config);
    },

    /**
     * A machine joins with a join token. The token is spent.
     */
    join: async (body: types.NodeJoinBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NodeJoined> => {
      let __path = `/api/v1/nodes/join`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["NodeJoinBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NodeJoined"],
      };

      return this.request<types.NodeJoined>(config);
    },

    /**
     * The machines joined to this organization
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NodeList> => {
      let __path = `/api/v1/nodes`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NodeList"],
      };

      return this.request<types.NodeList>(config);
    },

    /**
     * Stop trusting a node, at once. Its key can't connect again.
     */
    revoke: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Node> => {
      let __path = `/api/v1/nodes/${encodeURIComponent(String(id))}/revoke`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Node"],
      };

      return this.request<types.Node>(config);
    },

    /**
     * Rename a node, relabel it, narrow what it serves or set its ingress mode
     */
    update: async (id: string, body: types.NodeUpdateBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Node> => {
      let __path = `/api/v1/nodes/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["NodeUpdateBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Node"],
      };

      return this.request<types.Node>(config);
    },

    /**
     * What Loomplane runs on a node, and which databases have no backups
     */
    workloads: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NodeWorkloadList> => {
      let __path = `/api/v1/nodes/${encodeURIComponent(String(id))}/workloads`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NodeWorkloadList"],
      };

      return this.request<types.NodeWorkloadList>(config);
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
    create: async (cloudId: string, body: types.CreateTargetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudprofilesTarget> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/targets`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateTargetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudprofilesTarget"],
      };

      return this.request<types.CloudprofilesTarget>(config);
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
    get: async (cloudId: string, targetId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudprofilesTarget> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudprofilesTarget"],
      };

      return this.request<types.CloudprofilesTarget>(config);
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
    update: async (cloudId: string, targetId: string, body: types.UpdateTargetInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudprofilesTarget> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}/targets/${encodeURIComponent(String(targetId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateTargetInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudprofilesTarget"],
      };

      return this.request<types.CloudprofilesTarget>(config);
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
    update: async (cloudId: string, body: types.CloudprofilesUpdateInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Profile> => {
      let __path = `/api/v1/clouds/${encodeURIComponent(String(cloudId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["CloudprofilesUpdateInputBody"],
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

  public readonly placementGroups = {
    /**
     * placementGroups.destinationMapping
     */
    destinationMapping: async (id: string, body: types.GroupDestinationMappingInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GroupVerificationResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/placement-groups/destination-mappings`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["GroupDestinationMappingInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GroupVerificationResult"],
      };

      return this.request<types.GroupVerificationResult>(config);
    },

    /**
     * placementGroups.prerequisite
     */
    prerequisite: async (id: string, body: types.MemberPrerequisiteInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.MemberPrerequisite> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/placement-groups/prerequisites`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["MemberPrerequisiteInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["MemberPrerequisite"],
      };

      return this.request<types.MemberPrerequisite>(config);
    },

    /**
     * placementGroups.renewalAuthority
     */
    renewalAuthority: async (body: types.TLSRenewalAuthorityInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.TLSRenewalAuthority> => {
      let __path = `/api/v1/placement-groups/renewal-authorities`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["TLSRenewalAuthorityInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["TLSRenewalAuthority"],
      };

      return this.request<types.TLSRenewalAuthority>(config);
    },

    /**
     * placementGroups.timeoutVerification
     */
    timeoutVerification: async (id: string, body: types.TimeoutVerificationInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.GroupVerificationResult> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/placement-groups/timeout-verifications`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["TimeoutVerificationInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["GroupVerificationResult"],
      };

      return this.request<types.GroupVerificationResult>(config);
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
     * Plan an environment from the current manifest and one commit of each repository, or what another environment runs, replacing its open plan; a scope moves only some of them
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

    /**
     * How long the promotion's source environment has run exactly the commits the plan moves, and whether it ran them healthily; unverified means missing evidence
     */
    soak: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Soak> => {
      let __path = `/api/v1/plans/${encodeURIComponent(String(id))}/soak`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Soak"],
      };

      return this.request<types.Soak>(config);
    },

  };

  public readonly platformCloudHosts = {
    /**
     * Check a host now and record its evidence
     */
    check: async (host: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Host> => {
      let __path = `/api/v1/platform/cloud/hosts/${encodeURIComponent(String(host))}/check`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Host"],
      };

      return this.request<types.Host>(config);
    },

    /**
     * Register a host. The client certificate its agent must trust is returned this once
     */
    create: async (body: types.HostInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.NewHost> => {
      let __path = `/api/v1/platform/cloud/hosts`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["HostInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["NewHost"],
      };

      return this.request<types.NewHost>(config);
    },

    /**
     * Stop new placements on a host. What runs there keeps running
     */
    drain: async (host: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Host> => {
      let __path = `/api/v1/platform/cloud/hosts/${encodeURIComponent(String(host))}/drain`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Host"],
      };

      return this.request<types.Host>(config);
    },

    /**
     * Every host, with its evidence, allotment and what its placements use
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.HostList> => {
      let __path = `/api/v1/platform/cloud/hosts`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["HostList"],
      };

      return this.request<types.HostList>(config);
    },

    /**
     * Drain a host and move its services to other hosts one at a time. A service with nowhere to go stays where it is
     */
    moveAll: async (host: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.MoveAll> => {
      let __path = `/api/v1/platform/cloud/hosts/${encodeURIComponent(String(host))}/move-all`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["MoveAll"],
      };

      return this.request<types.MoveAll>(config);
    },

    /**
     * Take a drained, empty host out of service for good
     */
    retire: async (host: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/platform/cloud/hosts/${encodeURIComponent(String(host))}/retire`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        allowEmptyBody: true,
      };

      await this.request(config);
    },

    /**
     * Change how much of a host the platform may use. The host is checked again before it takes placements
     */
    setAllotment: async (host: string, body: types.AllotmentBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Host> => {
      let __path = `/api/v1/platform/cloud/hosts/${encodeURIComponent(String(host))}/allotment`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["AllotmentBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Host"],
      };

      return this.request<types.Host>(config);
    },

  };

  public readonly platformCloudRegions = {
    /**
     * Add a region
     */
    create: async (body: types.RegionInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Region> => {
      let __path = `/api/v1/platform/cloud/regions`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RegionInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Region"],
      };

      return this.request<types.Region>(config);
    },

    /**
     * Every platform Cloud region
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RegionList> => {
      let __path = `/api/v1/platform/cloud/regions`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["RegionList"],
      };

      return this.request<types.RegionList>(config);
    },

    /**
     * Rename a region for display, or open or close it
     */
    update: async (region: string, body: types.RegionUpdateBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Region> => {
      let __path = `/api/v1/platform/cloud/regions/${encodeURIComponent(String(region))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["RegionUpdateBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Region"],
      };

      return this.request<types.Region>(config);
    },

  };

  public readonly platformCloudSettings = {
    /**
     * The platform Cloud settings. The registry and DNS credentials are never returned
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SettingsView> => {
      let __path = `/api/v1/platform/cloud/settings`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SettingsView"],
      };

      return this.request<types.SettingsView>(config);
    },

    /**
     * Replace the platform Cloud settings, and the registry or DNS credential when given
     */
    set: async (body: types.SettingsInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SettingsView> => {
      let __path = `/api/v1/platform/cloud/settings`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["SettingsInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SettingsView"],
      };

      return this.request<types.SettingsView>(config);
    },

  };

  public readonly platformCloudSignInApps = {
    /**
     * Remove the platform's sign-in app for a cloud
     */
    delete: async (cloud: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/platform/cloud-sign-in/apps/${encodeURIComponent(String(cloud))}`;
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
     * The clouds people can sign in to, where each one's app comes from, how many sign-ins use it, and the callback URL to register
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SignInAppList> => {
      let __path = `/api/v1/platform/cloud-sign-in/apps`;
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
     * Set the platform's sign-in app for a cloud. The secret is never returned
     */
    set: async (cloud: string, body: types.AppInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SignInApp> => {
      let __path = `/api/v1/platform/cloud-sign-in/apps/${encodeURIComponent(String(cloud))}`;
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

  public readonly platformCloudTenants = {
    /**
     * Every organization's standing with the platform Cloud and its use against its limits
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlatformcloudTenantList> => {
      let __path = `/api/v1/platform/cloud/tenants`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PlatformcloudTenantList"],
      };

      return this.request<types.PlatformcloudTenantList>(config);
    },

    /**
     * Lift the platform owner's suspension. The work starts again within a minute, once nothing else stops it
     */
    restore: async (org: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlatformcloudTenant> => {
      let __path = `/api/v1/platform/cloud/tenants/${encodeURIComponent(String(org))}/restore`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PlatformcloudTenant"],
      };

      return this.request<types.PlatformcloudTenant>(config);
    },

    /**
     * Enable or disable an organization and set its limits
     */
    set: async (org: string, body: types.PlatformcloudTenantInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlatformcloudTenant> => {
      let __path = `/api/v1/platform/cloud/tenants/${encodeURIComponent(String(org))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["PlatformcloudTenantInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PlatformcloudTenant"],
      };

      return this.request<types.PlatformcloudTenant>(config);
    },

    /**
     * Stop an organization's platform work as the platform owner
     */
    suspend: async (org: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PlatformcloudTenant> => {
      let __path = `/api/v1/platform/cloud/tenants/${encodeURIComponent(String(org))}/suspend`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["PlatformcloudTenant"],
      };

      return this.request<types.PlatformcloudTenant>(config);
    },

  };

  public readonly projectAutonomy = {
    /**
     * Set whether the agent may promote a project's releases to production without asking, and the staging soak it needs
     * In this version the setting is stored and every production promotion still waits for a person; it takes effect when the promotion gate ships. Only a person with deployment:approve on the project can change it; the agent never can. It covers promoting a release someone asked for, after its staging soak, and never deleting or replacing production data or resources, which always waits for a person.
     */
    update: async (id: string, body: types.AutonomyInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Project> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/autonomy`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["AutonomyInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Project"],
      };

      return this.request<types.Project>(config);
    },

  };

  public readonly projectCloudLocations = {
    /**
     * projectCloudLocations.get
     */
    get: async (id: string, cloudId: string, locationId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudLocation> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/locations/${encodeURIComponent(String(locationId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudLocation"],
      };

      return this.request<types.CloudLocation>(config);
    },

    /**
     * projectCloudLocations.list
     */
    list: async (id: string, cloudId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.CloudLocationList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/clouds/${encodeURIComponent(String(cloudId))}/locations`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["CloudLocationList"],
      };

      return this.request<types.CloudLocationList>(config);
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
     * What the caller may do on one project, team grants included
     */
    permissions: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProjectPermissions> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/permissions`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProjectPermissions"],
      };

      return this.request<types.ProjectPermissions>(config);
    },

    /**
     * Name the team responsible for a project, or clear it with null
     */
    setTeam: async (id: string, body: types.SetTeamInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Project> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/team`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["SetTeamInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Project"],
      };

      return this.request<types.Project>(config);
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

  public readonly providerResources = {
    /**
     * Store an application DSN for a verified provider resource
     */
    connect: async (resourceId: string, body: types.ProviderResourceConnectInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProviderResource> => {
      let __path = `/api/v1/provider-resources/${encodeURIComponent(String(resourceId))}/connection`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["ProviderResourceConnectInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProviderResource"],
      };

      return this.request<types.ProviderResource>(config);
    },

    /**
     * Read a provider resource without its credentials
     */
    get: async (resourceId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProviderResource> => {
      let __path = `/api/v1/provider-resources/${encodeURIComponent(String(resourceId))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProviderResource"],
      };

      return this.request<types.ProviderResource>(config);
    },

    /**
     * List your provider resource inventory and sync attempts
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProviderResourceList> => {
      let __path = `/api/v1/provider-resources`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProviderResourceList"],
      };

      return this.request<types.ProviderResourceList>(config);
    },

    /**
     * Refresh a connected account's resource inventory
     */
    sync: async (connectionId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ProviderResourceSync> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(connectionId))}/resource-syncs`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ProviderResourceSync"],
      };

      return this.request<types.ProviderResourceSync>(config);
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

  public readonly push = {
    config: {
      /**
       * How this server sends push notifications
       */
      get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PushConfig> => {
        let __path = `/api/v1/push/config`;
        const config: RequestConfig = {
          method: 'GET',
          url: __path,
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["PushConfig"],
        };

        return this.request<types.PushConfig>(config);
      },

    },

    devices: {
      /**
       * Register this phone for push notifications
       */
      create: async (body: types.RegisterDeviceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PushDevice> => {
        let __path = `/api/v1/push/devices`;
        const config: RequestConfig = {
          method: 'POST',
          url: __path,
          body,
          bodyCodec: CODECS["RegisterDeviceInputBody"],
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["PushDevice"],
        };

        return this.request<types.PushDevice>(config);
      },

      /**
       * Stop sending push notifications to a phone
       */
      delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
        let __path = `/api/v1/push/devices/${encodeURIComponent(String(id))}`;
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
       * Send a test notification to a phone
       */
      test: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.TestPushResult> => {
        let __path = `/api/v1/push/devices/${encodeURIComponent(String(id))}/test`;
        const config: RequestConfig = {
          method: 'POST',
          url: __path,
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["TestPushResult"],
        };

        return this.request<types.TestPushResult>(config);
      },

      /**
       * Change a phone's time zone, quiet hours or settings
       */
      update: async (id: string, body: types.UpdateDeviceInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.PushDevice> => {
        let __path = `/api/v1/push/devices/${encodeURIComponent(String(id))}`;
        const config: RequestConfig = {
          method: 'PUT',
          url: __path,
          body,
          bodyCodec: CODECS["UpdateDeviceInputBody"],
          signal: options?.signal,
          retry: options?.retry,
          responseCodec: CODECS["PushDevice"],
        };

        return this.request<types.PushDevice>(config);
      },

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

  public readonly registries = {
    /**
     * Queue registry maintenance
     */
    action: async (connectionId: string, body: types.RegistryActionInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ManagedRegistryView> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(connectionId))}/registry/actions`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RegistryActionInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ManagedRegistryView"],
      };

      return this.request<types.ManagedRegistryView>(config);
    },

    /**
     * Read registry DNS, maintenance and backups
     */
    get: async (connectionId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ManagedRegistryView> => {
      let __path = `/api/v1/connections/${encodeURIComponent(String(connectionId))}/registry`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ManagedRegistryView"],
      };

      return this.request<types.ManagedRegistryView>(config);
    },

  };

  public readonly releaseReports = {
    /**
     * Stop a release preparation
     * The person who asked for it, while they may use the agent on the project, or someone who may approve its deployments. What already happened stands: a report that has finished is a 409. The plan is left for a person to decide.
     */
    cancel: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ReleaseReport> => {
      let __path = `/api/v1/release-reports/${encodeURIComponent(String(id))}/cancel`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ReleaseReport"],
      };

      return this.request<types.ReleaseReport>(config);
    },

    /**
     * One release report
     * Needs deployment:read on the report's project.
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ReleaseReport> => {
      let __path = `/api/v1/release-reports/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ReleaseReport"],
      };

      return this.request<types.ReleaseReport>(config);
    },

    /**
     * A project's release reports
     * Needs deployment:read on the project. Newest first.
     */
    list: async (id: string, limit?: number | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ReleaseReportList> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/release-reports`;
      const queryParams: Record<string, any> = {};
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
        responseCodec: CODECS["ReleaseReportList"],
      };

      return this.request<types.ReleaseReportList>(config);
    },

    /**
     * Ask the agent to prepare a production release
     * Needs agent:use and deployment:create on the project, as a person. Loomplane plans the promotion from staging, collects the evidence and the agent reviews it. Only someone who may approve production gets a release promoted without a second person; anyone else's waits for an approver in Needs approval. A plan waiting for a decision on production is a 409 naming it, unless you set replace.
     */
    prepare: async (id: string, body: types.PrepareInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ReleaseReport> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/release-reports`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["PrepareInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ReleaseReport"],
      };

      return this.request<types.ReleaseReport>(config);
    },

  };

  public readonly releaseWatches = {
    /**
     * How long a release in the environment would be watched, which services a canary would ramp, and whether each other service can be watched
     */
    coverage: async (id: string, environment: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Coverage> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(environment))}/watch-coverage`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Coverage"],
      };

      return this.request<types.Coverage>(config);
    },

    /**
     * What the post-deploy watch says about the environment's current release; unverified means missing evidence
     */
    evidence: async (id: string, environment: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.ReleaseEvidence> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(environment))}/release-evidence`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["ReleaseEvidence"],
      };

      return this.request<types.ReleaseEvidence>(config);
    },

    /**
     * The deployment's post-deploy watch: the services it judges, their verdicts and any automatic rollback; 404 when it has none
     */
    forDeployment: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Watch> => {
      let __path = `/api/v1/deployments/${encodeURIComponent(String(id))}/watch`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Watch"],
      };

      return this.request<types.Watch>(config);
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

  public readonly rollbacks = {
    /**
     * Plan the environment back to its previous successful release, with a reason; a person approves it like any plan. Data-bearing resources and services stay as they are
     */
    create: async (id: string, body: types.RollbackInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Plan> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/rollbacks`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["RollbackInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Plan"],
      };

      return this.request<types.Plan>(config);
    },

    /**
     * What rolling the environment back would undo and restore, and each repository's commits; nothing changes
     */
    target: async (id: string, environment: string, deploymentId?: string | undefined, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.RollbackTarget> => {
      let __path = `/api/v1/projects/${encodeURIComponent(String(id))}/environments/${encodeURIComponent(String(environment))}/rollback-target`;
      const queryParams: Record<string, any> = {};
      if (deploymentId !== undefined) {
        queryParams['deployment_id'] = deploymentId;
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
        responseCodec: CODECS["RollbackTarget"],
      };

      return this.request<types.RollbackTarget>(config);
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

  public readonly setup = {
    /**
     * Create the first account, make it the platform owner, and create its organization
     */
    complete: async (body: types.SetupRequestBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SetupResponse> => {
      let __path = `/api/v1/setup`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["SetupRequestBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SetupResponse"],
      };

      return this.request<types.SetupResponse>(config);
    },

    /**
     * Stop offering to set up cloud sign-in
     */
    endCloudSignInOffer: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/setup/cloud-sign-in-offer`;
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
     * Whether this instance still waits for its first account
     */
    status: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.SetupStatus> => {
      let __path = `/api/v1/setup`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["SetupStatus"],
      };

      return this.request<types.SetupStatus>(config);
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

  public readonly teams = {
    /**
     * Put a workspace member in the team
     */
    addMember: async (id: string, userId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Team> => {
      let __path = `/api/v1/teams/${encodeURIComponent(String(id))}/members/${encodeURIComponent(String(userId))}`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Team"],
      };

      return this.request<types.Team>(config);
    },

    /**
     * Create a team
     */
    create: async (body: types.CreateTeamInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Team> => {
      let __path = `/api/v1/teams`;
      const config: RequestConfig = {
        method: 'POST',
        url: __path,
        body,
        bodyCodec: CODECS["CreateTeamInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Team"],
      };

      return this.request<types.Team>(config);
    },

    /**
     * Delete a team. Its members lose the roles it gave them, and projects it was responsible for have none.
     */
    delete: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<void> => {
      let __path = `/api/v1/teams/${encodeURIComponent(String(id))}`;
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
     * One team
     */
    get: async (id: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Team> => {
      let __path = `/api/v1/teams/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Team"],
      };

      return this.request<types.Team>(config);
    },

    /**
     * The workspace's teams, with their members and grants
     */
    list: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.TeamList> => {
      let __path = `/api/v1/teams`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["TeamList"],
      };

      return this.request<types.TeamList>(config);
    },

    /**
     * Take one of the team's roles away from its members
     */
    removeGrant: async (id: string, grantId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Team> => {
      let __path = `/api/v1/teams/${encodeURIComponent(String(id))}/grants/${encodeURIComponent(String(grantId))}`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Team"],
      };

      return this.request<types.Team>(config);
    },

    /**
     * Take a person out of the team
     */
    removeMember: async (id: string, userId: string, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Team> => {
      let __path = `/api/v1/teams/${encodeURIComponent(String(id))}/members/${encodeURIComponent(String(userId))}`;
      const config: RequestConfig = {
        method: 'DELETE',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Team"],
      };

      return this.request<types.Team>(config);
    },

    /**
     * Give the team a role on a project, or on the whole workspace without a project. Never above your own role.
     */
    setGrant: async (id: string, body: types.SetGrantInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Team> => {
      let __path = `/api/v1/teams/${encodeURIComponent(String(id))}/grants`;
      const config: RequestConfig = {
        method: 'PUT',
        url: __path,
        body,
        bodyCodec: CODECS["SetGrantInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Team"],
      };

      return this.request<types.Team>(config);
    },

    /**
     * Rename a team or change its description
     */
    update: async (id: string, body: types.UpdateTeamInputBody, options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.Team> => {
      let __path = `/api/v1/teams/${encodeURIComponent(String(id))}`;
      const config: RequestConfig = {
        method: 'PATCH',
        url: __path,
        body,
        bodyCodec: CODECS["UpdateTeamInputBody"],
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["Team"],
      };

      return this.request<types.Team>(config);
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

  public readonly workspacePlatformCloud = {
    /**
     * Your workspace's Loomplane Cloud: whether it's enabled, why it's suspended, limits, use, regions and placements
     */
    get: async (options?: { signal?: AbortSignal; retry?: { maxAttempts?: number } }): Promise<types.WorkspaceView> => {
      let __path = `/api/v1/platform-cloud`;
      const config: RequestConfig = {
        method: 'GET',
        url: __path,
        signal: options?.signal,
        retry: options?.retry,
        responseCodec: CODECS["WorkspaceView"],
      };

      return this.request<types.WorkspaceView>(config);
    },

  };

}
