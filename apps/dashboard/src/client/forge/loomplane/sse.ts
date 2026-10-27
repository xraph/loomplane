// Browser/Node.js compatibility layer
const isBrowser = typeof window !== 'undefined' && typeof window.EventSource !== 'undefined';

// Lazy-loaded EventSource implementation
let _EventSourceImpl: typeof EventSource | null = null;

// Node.js CommonJS fallback. Phase 4 replaces this with dynamic import().
declare const require: ((id: string) => any) | undefined;

function getEventSource(): typeof EventSource {
  if (_EventSourceImpl) return _EventSourceImpl;
  
  if (isBrowser) {
    _EventSourceImpl = window.EventSource;
  } else {
    // Node.js - use dynamic require for compatibility
    try {
      if (typeof require === 'undefined') {
        throw new Error('No EventSource implementation available in this environment.');
      }
      // eslint-disable-next-line @typescript-eslint/no-var-requires
      const esModule = require('eventsource');
      _EventSourceImpl = esModule.default || esModule;
    } catch {
      throw new Error('EventSource implementation not found. Install "eventsource" package for Node.js.');
    }
  }
  return _EventSourceImpl!;
}

// Simple EventEmitter for cross-platform support
class EventEmitter {
  private listeners: Map<string, Set<Function>> = new Map();

  on(event: string, handler: Function): void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(handler);
  }

  off(event: string, handler: Function): void {
    this.listeners.get(event)?.delete(handler);
  }

  emit(event: string, ...args: any[]): void {
    const handlers = this.listeners.get(event);
    if (handlers) {
      handlers.forEach(handler => {
        try {
          handler(...args);
        } catch (error) {
          console.error('Event handler error:', error);
        }
      });
    }
  }

  removeAllListeners(event?: string): void {
    if (event) {
      this.listeners.delete(event);
    } else {
      this.listeners.clear();
    }
  }
}


/** Configuration for SSE client */
export interface SSEClientConfig {
  /** Base URL for SSE connection */
  baseURL: string;
  /** Authentication configuration */
  auth?: types.AuthConfig;
  /** Connection timeout in ms (default: 30000) */
  connectionTimeout?: number;
  /** Whether to include credentials (cookies) in browser (default: false) */
  withCredentials?: boolean;
}

import * as types from './types';
import { ConnectionState } from './types';
import { decode } from './codecs';

/**
 * RealtimeEventsSSEClient
 * 
 * Features:
 * - Cross-platform (Browser & Node.js)
 * - Connection timeouts
 */
export class RealtimeEventsSSEClient extends EventEmitter {
  private eventSource: EventSource | null = null;
  private config: Required<Pick<SSEClientConfig, 'baseURL'>> & SSEClientConfig;
  private state: ConnectionState = ConnectionState.DISCONNECTED;
  private connectionTimeoutId: ReturnType<typeof setTimeout> | null = null;

  constructor(config: SSEClientConfig) {
    super();
    this.config = {
      connectionTimeout: 30000,
      withCredentials: false,
      ...config,
    };
  }

  /**
   * Connect to the SSE stream.
   * @returns Promise that resolves when connected
   * @throws Error if connection fails or times out
   */
  async connect(): Promise<void> {
    if (this.state === ConnectionState.CONNECTED) {
      return;
    }

    return new Promise((resolve, reject) => {
      this.setState(ConnectionState.CONNECTING);
      let url = this.config.baseURL + '/api/v1/realtime/events';
      
      // In browser, add auth as query params since EventSource doesn't support custom headers
      if (isBrowser) {
        const params = new URLSearchParams();
        if (this.config.auth?.bearerToken) {
          params.set('token', this.config.auth.bearerToken);
        }
        if (this.config.auth?.apiKey) {
          params.set('apiKey', this.config.auth.apiKey);
        }
        const queryString = params.toString();
        if (queryString) {
          url += (url.includes('?') ? '&' : '?') + queryString;
        }
      }

      // Setup connection timeout
      this.connectionTimeoutId = setTimeout(() => {
        this.connectionTimeoutId = null;
        if (this.eventSource) {
          this.eventSource.close();
          this.eventSource = null;
        }
        this.setState(ConnectionState.ERROR);
        reject(new Error('Connection timeout'));
      }, this.config.connectionTimeout);

      try {
        const ES = getEventSource();

        if (isBrowser) {
          this.eventSource = new ES(url, {
            withCredentials: this.config.withCredentials,
          });
        } else {
          // Node.js: can pass custom headers
          const headers: Record<string, string> = {
            'Accept': 'text/event-stream',
            'Cache-Control': 'no-cache',
          };
          if (this.config.auth?.bearerToken) {
            headers['Authorization'] = `Bearer ${this.config.auth.bearerToken}`;
          }
          if (this.config.auth?.apiKey) {
            headers['X-API-Key'] = this.config.auth.apiKey;
          }
          this.eventSource = new (ES as any)(url, { headers }) as EventSource;
        }

        this.eventSource.onopen = () => {
          this.clearConnectionTimeout();
          this.setState(ConnectionState.CONNECTED);
          resolve();
        };

        this.eventSource.onerror = (event: Event) => {
          this.clearConnectionTimeout();
          this.setState(ConnectionState.ERROR);
          const error = new Error('SSE connection error');
          this.emit('error', error);

          reject(error);
        };

        this.eventSource.addEventListener('analysis.deleted', (event: MessageEvent) => {
          try {
            const data: types.Analysis = decode(JSON.parse(event.data), "Analysis");
            this.emit('analysis.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('analysis.updated', (event: MessageEvent) => {
          try {
            const data: types.Analysis = decode(JSON.parse(event.data), "Analysis");
            this.emit('analysis.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('binding.deleted', (event: MessageEvent) => {
          try {
            const data: types.Binding = decode(JSON.parse(event.data), "Binding");
            this.emit('binding.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('binding.updated', (event: MessageEvent) => {
          try {
            const data: types.Binding = decode(JSON.parse(event.data), "Binding");
            this.emit('binding.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('build.collection', (event: MessageEvent) => {
          try {
            const data: types.Collection = decode(JSON.parse(event.data), "Collection");
            this.emit('build.collection', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('build.log', (event: MessageEvent) => {
          try {
            const data: types.Log = decode(JSON.parse(event.data), "Log");
            this.emit('build.log', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('canary_verdict.deleted', (event: MessageEvent) => {
          try {
            const data: types.CanaryVerdict = decode(JSON.parse(event.data), "CanaryVerdict");
            this.emit('canary_verdict.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('canary_verdict.updated', (event: MessageEvent) => {
          try {
            const data: types.CanaryVerdict = decode(JSON.parse(event.data), "CanaryVerdict");
            this.emit('canary_verdict.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('cloud.deleted', (event: MessageEvent) => {
          try {
            const data: types.Profile = decode(JSON.parse(event.data), "Profile");
            this.emit('cloud.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('cloud.updated', (event: MessageEvent) => {
          try {
            const data: types.Profile = decode(JSON.parse(event.data), "Profile");
            this.emit('cloud.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('cloud_sync.updated', (event: MessageEvent) => {
          try {
            const data: types.CloudSyncEvent = decode(JSON.parse(event.data), "CloudSyncEvent");
            this.emit('cloud_sync.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('cloud_target.deleted', (event: MessageEvent) => {
          try {
            const data: types.Target = decode(JSON.parse(event.data), "Target");
            this.emit('cloud_target.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('cloud_target.updated', (event: MessageEvent) => {
          try {
            const data: types.Target = decode(JSON.parse(event.data), "Target");
            this.emit('cloud_target.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('connection.deleted', (event: MessageEvent) => {
          try {
            const data: types.Connection = decode(JSON.parse(event.data), "Connection");
            this.emit('connection.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('connection.updated', (event: MessageEvent) => {
          try {
            const data: types.Connection = decode(JSON.parse(event.data), "Connection");
            this.emit('connection.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('deployment.deleted', (event: MessageEvent) => {
          try {
            const data: types.Deployment = decode(JSON.parse(event.data), "Deployment");
            this.emit('deployment.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('deployment.updated', (event: MessageEvent) => {
          try {
            const data: types.Deployment = decode(JSON.parse(event.data), "Deployment");
            this.emit('deployment.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('environment.deleted', (event: MessageEvent) => {
          try {
            const data: types.Environment = decode(JSON.parse(event.data), "Environment");
            this.emit('environment.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('environment.updated', (event: MessageEvent) => {
          try {
            const data: types.Environment = decode(JSON.parse(event.data), "Environment");
            this.emit('environment.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('environment_cloud.updated', (event: MessageEvent) => {
          try {
            const data: types.EnvironmentCloudEvent = decode(JSON.parse(event.data), "EnvironmentCloudEvent");
            this.emit('environment_cloud.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('forge.gap', (event: MessageEvent) => {
          try {
            const data: types.RealtimeForgeGap = decode(JSON.parse(event.data), "RealtimeForgeGap");
            this.emit('forge.gap', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('forge.resumed', (event: MessageEvent) => {
          try {
            const data: types.RealtimeForgeResumed = decode(JSON.parse(event.data), "RealtimeForgeResumed");
            this.emit('forge.resumed', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('manifest.deleted', (event: MessageEvent) => {
          try {
            const data: types.Revision = decode(JSON.parse(event.data), "Revision");
            this.emit('manifest.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('manifest.updated', (event: MessageEvent) => {
          try {
            const data: types.Revision = decode(JSON.parse(event.data), "Revision");
            this.emit('manifest.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('plan.deleted', (event: MessageEvent) => {
          try {
            const data: types.Plan = decode(JSON.parse(event.data), "Plan");
            this.emit('plan.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('plan.updated', (event: MessageEvent) => {
          try {
            const data: types.Plan = decode(JSON.parse(event.data), "Plan");
            this.emit('plan.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('project.deleted', (event: MessageEvent) => {
          try {
            const data: types.Project = decode(JSON.parse(event.data), "Project");
            this.emit('project.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('project.updated', (event: MessageEvent) => {
          try {
            const data: types.Project = decode(JSON.parse(event.data), "Project");
            this.emit('project.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('realtime.snapshot', (event: MessageEvent) => {
          try {
            const data: types.RealtimeRealtimeSnapshot = decode(JSON.parse(event.data), "RealtimeRealtimeSnapshot");
            this.emit('realtime.snapshot', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('realtime.status', (event: MessageEvent) => {
          try {
            const data: types.RealtimeRealtimeStatus = decode(JSON.parse(event.data), "RealtimeRealtimeStatus");
            this.emit('realtime.status', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('source.deleted', (event: MessageEvent) => {
          try {
            const data: types.Source = decode(JSON.parse(event.data), "Source");
            this.emit('source.deleted', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        this.eventSource.addEventListener('source.updated', (event: MessageEvent) => {
          try {
            const data: types.Source = decode(JSON.parse(event.data), "Source");
            this.emit('source.updated', data);
          } catch (error) {
            this.emit('error', error);
          }
        });

        // Listen for generic messages
        this.eventSource.onmessage = (event: MessageEvent) => {
          this.emit('message', event.data);
        };
      } catch (error) {
        this.clearConnectionTimeout();
        this.setState(ConnectionState.ERROR);
        reject(error);
      }
    });
  }

  /**
   * Register a handler for analysis.deleted events.
   * @param handler - Function to call when analysis.deleted event is received
   */
  onAnalysisDeleted(handler: (data: types.Analysis) => void): void {
    this.on('analysis.deleted', handler);
  }

  /**
   * Remove a handler for analysis.deleted events.
   * @param handler - The handler to remove
   */
  offAnalysisDeleted(handler: (data: types.Analysis) => void): void {
    this.off('analysis.deleted', handler);
  }

  /**
   * Register a handler for analysis.updated events.
   * @param handler - Function to call when analysis.updated event is received
   */
  onAnalysisUpdated(handler: (data: types.Analysis) => void): void {
    this.on('analysis.updated', handler);
  }

  /**
   * Remove a handler for analysis.updated events.
   * @param handler - The handler to remove
   */
  offAnalysisUpdated(handler: (data: types.Analysis) => void): void {
    this.off('analysis.updated', handler);
  }

  /**
   * Register a handler for binding.deleted events.
   * @param handler - Function to call when binding.deleted event is received
   */
  onBindingDeleted(handler: (data: types.Binding) => void): void {
    this.on('binding.deleted', handler);
  }

  /**
   * Remove a handler for binding.deleted events.
   * @param handler - The handler to remove
   */
  offBindingDeleted(handler: (data: types.Binding) => void): void {
    this.off('binding.deleted', handler);
  }

  /**
   * Register a handler for binding.updated events.
   * @param handler - Function to call when binding.updated event is received
   */
  onBindingUpdated(handler: (data: types.Binding) => void): void {
    this.on('binding.updated', handler);
  }

  /**
   * Remove a handler for binding.updated events.
   * @param handler - The handler to remove
   */
  offBindingUpdated(handler: (data: types.Binding) => void): void {
    this.off('binding.updated', handler);
  }

  /**
   * Register a handler for build.collection events.
   * @param handler - Function to call when build.collection event is received
   */
  onBuildCollection(handler: (data: types.Collection) => void): void {
    this.on('build.collection', handler);
  }

  /**
   * Remove a handler for build.collection events.
   * @param handler - The handler to remove
   */
  offBuildCollection(handler: (data: types.Collection) => void): void {
    this.off('build.collection', handler);
  }

  /**
   * Register a handler for build.log events.
   * @param handler - Function to call when build.log event is received
   */
  onBuildLog(handler: (data: types.Log) => void): void {
    this.on('build.log', handler);
  }

  /**
   * Remove a handler for build.log events.
   * @param handler - The handler to remove
   */
  offBuildLog(handler: (data: types.Log) => void): void {
    this.off('build.log', handler);
  }

  /**
   * Register a handler for canary_verdict.deleted events.
   * @param handler - Function to call when canary_verdict.deleted event is received
   */
  onCanaryVerdictDeleted(handler: (data: types.CanaryVerdict) => void): void {
    this.on('canary_verdict.deleted', handler);
  }

  /**
   * Remove a handler for canary_verdict.deleted events.
   * @param handler - The handler to remove
   */
  offCanaryVerdictDeleted(handler: (data: types.CanaryVerdict) => void): void {
    this.off('canary_verdict.deleted', handler);
  }

  /**
   * Register a handler for canary_verdict.updated events.
   * @param handler - Function to call when canary_verdict.updated event is received
   */
  onCanaryVerdictUpdated(handler: (data: types.CanaryVerdict) => void): void {
    this.on('canary_verdict.updated', handler);
  }

  /**
   * Remove a handler for canary_verdict.updated events.
   * @param handler - The handler to remove
   */
  offCanaryVerdictUpdated(handler: (data: types.CanaryVerdict) => void): void {
    this.off('canary_verdict.updated', handler);
  }

  /**
   * Register a handler for cloud.deleted events.
   * @param handler - Function to call when cloud.deleted event is received
   */
  onCloudDeleted(handler: (data: types.Profile) => void): void {
    this.on('cloud.deleted', handler);
  }

  /**
   * Remove a handler for cloud.deleted events.
   * @param handler - The handler to remove
   */
  offCloudDeleted(handler: (data: types.Profile) => void): void {
    this.off('cloud.deleted', handler);
  }

  /**
   * Register a handler for cloud.updated events.
   * @param handler - Function to call when cloud.updated event is received
   */
  onCloudUpdated(handler: (data: types.Profile) => void): void {
    this.on('cloud.updated', handler);
  }

  /**
   * Remove a handler for cloud.updated events.
   * @param handler - The handler to remove
   */
  offCloudUpdated(handler: (data: types.Profile) => void): void {
    this.off('cloud.updated', handler);
  }

  /**
   * Register a handler for cloud_sync.updated events.
   * @param handler - Function to call when cloud_sync.updated event is received
   */
  onCloudSyncUpdated(handler: (data: types.CloudSyncEvent) => void): void {
    this.on('cloud_sync.updated', handler);
  }

  /**
   * Remove a handler for cloud_sync.updated events.
   * @param handler - The handler to remove
   */
  offCloudSyncUpdated(handler: (data: types.CloudSyncEvent) => void): void {
    this.off('cloud_sync.updated', handler);
  }

  /**
   * Register a handler for cloud_target.deleted events.
   * @param handler - Function to call when cloud_target.deleted event is received
   */
  onCloudTargetDeleted(handler: (data: types.Target) => void): void {
    this.on('cloud_target.deleted', handler);
  }

  /**
   * Remove a handler for cloud_target.deleted events.
   * @param handler - The handler to remove
   */
  offCloudTargetDeleted(handler: (data: types.Target) => void): void {
    this.off('cloud_target.deleted', handler);
  }

  /**
   * Register a handler for cloud_target.updated events.
   * @param handler - Function to call when cloud_target.updated event is received
   */
  onCloudTargetUpdated(handler: (data: types.Target) => void): void {
    this.on('cloud_target.updated', handler);
  }

  /**
   * Remove a handler for cloud_target.updated events.
   * @param handler - The handler to remove
   */
  offCloudTargetUpdated(handler: (data: types.Target) => void): void {
    this.off('cloud_target.updated', handler);
  }

  /**
   * Register a handler for connection.deleted events.
   * @param handler - Function to call when connection.deleted event is received
   */
  onConnectionDeleted(handler: (data: types.Connection) => void): void {
    this.on('connection.deleted', handler);
  }

  /**
   * Remove a handler for connection.deleted events.
   * @param handler - The handler to remove
   */
  offConnectionDeleted(handler: (data: types.Connection) => void): void {
    this.off('connection.deleted', handler);
  }

  /**
   * Register a handler for connection.updated events.
   * @param handler - Function to call when connection.updated event is received
   */
  onConnectionUpdated(handler: (data: types.Connection) => void): void {
    this.on('connection.updated', handler);
  }

  /**
   * Remove a handler for connection.updated events.
   * @param handler - The handler to remove
   */
  offConnectionUpdated(handler: (data: types.Connection) => void): void {
    this.off('connection.updated', handler);
  }

  /**
   * Register a handler for deployment.deleted events.
   * @param handler - Function to call when deployment.deleted event is received
   */
  onDeploymentDeleted(handler: (data: types.Deployment) => void): void {
    this.on('deployment.deleted', handler);
  }

  /**
   * Remove a handler for deployment.deleted events.
   * @param handler - The handler to remove
   */
  offDeploymentDeleted(handler: (data: types.Deployment) => void): void {
    this.off('deployment.deleted', handler);
  }

  /**
   * Register a handler for deployment.updated events.
   * @param handler - Function to call when deployment.updated event is received
   */
  onDeploymentUpdated(handler: (data: types.Deployment) => void): void {
    this.on('deployment.updated', handler);
  }

  /**
   * Remove a handler for deployment.updated events.
   * @param handler - The handler to remove
   */
  offDeploymentUpdated(handler: (data: types.Deployment) => void): void {
    this.off('deployment.updated', handler);
  }

  /**
   * Register a handler for environment.deleted events.
   * @param handler - Function to call when environment.deleted event is received
   */
  onEnvironmentDeleted(handler: (data: types.Environment) => void): void {
    this.on('environment.deleted', handler);
  }

  /**
   * Remove a handler for environment.deleted events.
   * @param handler - The handler to remove
   */
  offEnvironmentDeleted(handler: (data: types.Environment) => void): void {
    this.off('environment.deleted', handler);
  }

  /**
   * Register a handler for environment.updated events.
   * @param handler - Function to call when environment.updated event is received
   */
  onEnvironmentUpdated(handler: (data: types.Environment) => void): void {
    this.on('environment.updated', handler);
  }

  /**
   * Remove a handler for environment.updated events.
   * @param handler - The handler to remove
   */
  offEnvironmentUpdated(handler: (data: types.Environment) => void): void {
    this.off('environment.updated', handler);
  }

  /**
   * Register a handler for environment_cloud.updated events.
   * @param handler - Function to call when environment_cloud.updated event is received
   */
  onEnvironmentCloudUpdated(handler: (data: types.EnvironmentCloudEvent) => void): void {
    this.on('environment_cloud.updated', handler);
  }

  /**
   * Remove a handler for environment_cloud.updated events.
   * @param handler - The handler to remove
   */
  offEnvironmentCloudUpdated(handler: (data: types.EnvironmentCloudEvent) => void): void {
    this.off('environment_cloud.updated', handler);
  }

  /**
   * Register a handler for forge.gap events.
   * @param handler - Function to call when forge.gap event is received
   */
  onForgeGap(handler: (data: types.RealtimeForgeGap) => void): void {
    this.on('forge.gap', handler);
  }

  /**
   * Remove a handler for forge.gap events.
   * @param handler - The handler to remove
   */
  offForgeGap(handler: (data: types.RealtimeForgeGap) => void): void {
    this.off('forge.gap', handler);
  }

  /**
   * Register a handler for forge.resumed events.
   * @param handler - Function to call when forge.resumed event is received
   */
  onForgeResumed(handler: (data: types.RealtimeForgeResumed) => void): void {
    this.on('forge.resumed', handler);
  }

  /**
   * Remove a handler for forge.resumed events.
   * @param handler - The handler to remove
   */
  offForgeResumed(handler: (data: types.RealtimeForgeResumed) => void): void {
    this.off('forge.resumed', handler);
  }

  /**
   * Register a handler for manifest.deleted events.
   * @param handler - Function to call when manifest.deleted event is received
   */
  onManifestDeleted(handler: (data: types.Revision) => void): void {
    this.on('manifest.deleted', handler);
  }

  /**
   * Remove a handler for manifest.deleted events.
   * @param handler - The handler to remove
   */
  offManifestDeleted(handler: (data: types.Revision) => void): void {
    this.off('manifest.deleted', handler);
  }

  /**
   * Register a handler for manifest.updated events.
   * @param handler - Function to call when manifest.updated event is received
   */
  onManifestUpdated(handler: (data: types.Revision) => void): void {
    this.on('manifest.updated', handler);
  }

  /**
   * Remove a handler for manifest.updated events.
   * @param handler - The handler to remove
   */
  offManifestUpdated(handler: (data: types.Revision) => void): void {
    this.off('manifest.updated', handler);
  }

  /**
   * Register a handler for plan.deleted events.
   * @param handler - Function to call when plan.deleted event is received
   */
  onPlanDeleted(handler: (data: types.Plan) => void): void {
    this.on('plan.deleted', handler);
  }

  /**
   * Remove a handler for plan.deleted events.
   * @param handler - The handler to remove
   */
  offPlanDeleted(handler: (data: types.Plan) => void): void {
    this.off('plan.deleted', handler);
  }

  /**
   * Register a handler for plan.updated events.
   * @param handler - Function to call when plan.updated event is received
   */
  onPlanUpdated(handler: (data: types.Plan) => void): void {
    this.on('plan.updated', handler);
  }

  /**
   * Remove a handler for plan.updated events.
   * @param handler - The handler to remove
   */
  offPlanUpdated(handler: (data: types.Plan) => void): void {
    this.off('plan.updated', handler);
  }

  /**
   * Register a handler for project.deleted events.
   * @param handler - Function to call when project.deleted event is received
   */
  onProjectDeleted(handler: (data: types.Project) => void): void {
    this.on('project.deleted', handler);
  }

  /**
   * Remove a handler for project.deleted events.
   * @param handler - The handler to remove
   */
  offProjectDeleted(handler: (data: types.Project) => void): void {
    this.off('project.deleted', handler);
  }

  /**
   * Register a handler for project.updated events.
   * @param handler - Function to call when project.updated event is received
   */
  onProjectUpdated(handler: (data: types.Project) => void): void {
    this.on('project.updated', handler);
  }

  /**
   * Remove a handler for project.updated events.
   * @param handler - The handler to remove
   */
  offProjectUpdated(handler: (data: types.Project) => void): void {
    this.off('project.updated', handler);
  }

  /**
   * Register a handler for realtime.snapshot events.
   * @param handler - Function to call when realtime.snapshot event is received
   */
  onRealtimeSnapshot(handler: (data: types.RealtimeRealtimeSnapshot) => void): void {
    this.on('realtime.snapshot', handler);
  }

  /**
   * Remove a handler for realtime.snapshot events.
   * @param handler - The handler to remove
   */
  offRealtimeSnapshot(handler: (data: types.RealtimeRealtimeSnapshot) => void): void {
    this.off('realtime.snapshot', handler);
  }

  /**
   * Register a handler for realtime.status events.
   * @param handler - Function to call when realtime.status event is received
   */
  onRealtimeStatus(handler: (data: types.RealtimeRealtimeStatus) => void): void {
    this.on('realtime.status', handler);
  }

  /**
   * Remove a handler for realtime.status events.
   * @param handler - The handler to remove
   */
  offRealtimeStatus(handler: (data: types.RealtimeRealtimeStatus) => void): void {
    this.off('realtime.status', handler);
  }

  /**
   * Register a handler for source.deleted events.
   * @param handler - Function to call when source.deleted event is received
   */
  onSourceDeleted(handler: (data: types.Source) => void): void {
    this.on('source.deleted', handler);
  }

  /**
   * Remove a handler for source.deleted events.
   * @param handler - The handler to remove
   */
  offSourceDeleted(handler: (data: types.Source) => void): void {
    this.off('source.deleted', handler);
  }

  /**
   * Register a handler for source.updated events.
   * @param handler - Function to call when source.updated event is received
   */
  onSourceUpdated(handler: (data: types.Source) => void): void {
    this.on('source.updated', handler);
  }

  /**
   * Remove a handler for source.updated events.
   * @param handler - The handler to remove
   */
  offSourceUpdated(handler: (data: types.Source) => void): void {
    this.off('source.updated', handler);
  }

  /**
   * Register a handler for generic messages.
   * @param handler - Function to call when a generic message is received
   */
  onMessage(handler: (data: string) => void): void {
    this.on('message', handler);
  }

  /**
   * Register a handler for errors.
   * @param handler - Function to call when an error occurs
   */
  onError(handler: (error: Error) => void): void {
    this.on('error', handler);
  }

  private setState(state: ConnectionState): void {
    if (this.state !== state) {
      this.state = state;
      this.emit('stateChange', state);
    }
  }

  private clearConnectionTimeout(): void {
    if (this.connectionTimeoutId) {
      clearTimeout(this.connectionTimeoutId);
      this.connectionTimeoutId = null;
    }
  }

  /**
   * Close the SSE connection.
   */
  close(): void {
    this.clearConnectionTimeout();

    if (this.eventSource) {
      this.eventSource.close();
      this.eventSource = null;
    }
    this.setState(ConnectionState.CLOSED);
  }

  /**
   * Check if the SSE stream is currently connected.
   */
  isConnected(): boolean {
    return this.state === ConnectionState.CONNECTED;
  }
}

