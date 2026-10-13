// Browser/Node.js compatibility layer
const isBrowser = typeof window !== 'undefined' && typeof window.WebSocket !== 'undefined';

// Lazy-loaded WebSocket implementation
let _WebSocketImpl: typeof WebSocket | null = null;

// Node.js CommonJS fallback. Phase 4 replaces this with dynamic import().
declare const require: ((id: string) => any) | undefined;

function getWebSocket(): typeof WebSocket {
  if (_WebSocketImpl) return _WebSocketImpl;
  
  if (isBrowser) {
    _WebSocketImpl = window.WebSocket;
  } else {
    // Node.js - use dynamic require for compatibility
    try {
      if (typeof require === 'undefined') {
        throw new Error('No WebSocket implementation available in this environment.');
      }
      // eslint-disable-next-line @typescript-eslint/no-var-requires
      _WebSocketImpl = require('ws');
    } catch {
      throw new Error('WebSocket implementation not found. Install "ws" package for Node.js.');
    }
  }
  return _WebSocketImpl!;
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


/** Message queued for sending when offline */
interface QueuedMessage {
  data: any;
  timestamp: number;
  resolve: () => void;
  reject: (error: Error) => void;
}

/** Configuration for WebSocket client */
export interface WebSocketClientConfig {
  /** Base URL for WebSocket connection */
  baseURL: string;
  /** Authentication configuration */
  auth?: types.AuthConfig;
  /** Connection timeout in ms (default: 30000) */
  connectionTimeout?: number;
  /** Request timeout in ms (default: 10000) */
  requestTimeout?: number;
  /** Enable offline message queue (default: true) */
  enableOfflineQueue?: boolean;
  /** Maximum messages in offline queue (default: 1000) */
  maxQueueSize?: number;
  /** Message TTL in queue in ms (default: 300000 - 5 min) */
  queueMessageTTL?: number;
}

import * as types from './types';
import { ConnectionState } from './types';
import { decode, encode } from './codecs';

/**
 * RealtimeWsReceiveWSClient
 * 
 * Features:
 * - Cross-platform (Browser & Node.js)
 * - Connection timeouts
 * - Offline message queue
 */
export class RealtimeWsReceiveWSClient extends EventEmitter {
  private ws: WebSocket | null = null;
  private config: Required<Pick<WebSocketClientConfig, 'baseURL'>> & WebSocketClientConfig;
  private state: ConnectionState = ConnectionState.DISCONNECTED;
  private closed: boolean = false;
  private connectionTimeoutId: ReturnType<typeof setTimeout> | null = null;
  private messageQueue: QueuedMessage[] = [];

  constructor(config: WebSocketClientConfig) {
    super();
    this.config = {
      connectionTimeout: 30000,
      requestTimeout: 10000,
      enableOfflineQueue: true,
      maxQueueSize: 1000,
      queueMessageTTL: 300000,
      ...config,
    };
  }

  /**
   * Connect to the WebSocket server.
   * @returns Promise that resolves when connected
   * @throws Error if connection fails or times out
   */
  async connect(): Promise<void> {
    if (this.state === ConnectionState.CONNECTED) {
      return;
    }

    return new Promise((resolve, reject) => {
      this.setState(ConnectionState.CONNECTING);
      this.closed = false;

      const wsURL = this.config.baseURL.replace(/^http/, 'ws') + '/api/v1/realtime/ws';

      // Setup connection timeout
      this.connectionTimeoutId = setTimeout(() => {
        this.connectionTimeoutId = null;
        if (this.ws) {
          this.ws.close();
          this.ws = null;
        }
        this.setState(ConnectionState.ERROR);
        reject(new Error('Connection timeout'));
      }, this.config.connectionTimeout);

      try {
        const WS = getWebSocket();
        
        // Build URL with auth if needed
        let url = wsURL;
        if (this.config.auth?.bearerToken) {
          const separator = url.includes('?') ? '&' : '?';
          url += `${separator}token=${encodeURIComponent(this.config.auth.bearerToken)}`;
        }

        if (isBrowser) {
          this.ws = new WS(url);
        } else {
          // Node.js: can pass headers
          const headers: Record<string, string> = {};
          if (this.config.auth?.bearerToken) {
            headers['Authorization'] = `Bearer ${this.config.auth.bearerToken}`;
          }
          if (this.config.auth?.apiKey) {
            headers['X-API-Key'] = this.config.auth.apiKey;
          }
          this.ws = new (WS as any)(url, { headers }) as WebSocket;
        }

        // Standard WebSocket event handlers (works in browser and Node.js)
        this.ws.onopen = () => {
          this.clearConnectionTimeout();
          this.setState(ConnectionState.CONNECTED);
          this.flushQueue();
          resolve();
        };

        this.ws.onmessage = (event: MessageEvent) => {
          try {
            const data = typeof event.data === 'string' ? event.data : event.data.toString();
            const message: types.RealtimeReceive = decode(JSON.parse(data), "RealtimeReceive");
            this.emit('message', message);
          } catch (error) {
            this.emit('error', error);
          }
        };

        this.ws.onerror = (event: Event) => {
          this.clearConnectionTimeout();
          this.setState(ConnectionState.ERROR);
          const error = new Error('WebSocket error');
          this.emit('error', error);
          reject(error);
        };

        this.ws.onclose = (event: CloseEvent) => {
          this.clearConnectionTimeout();
          
          if (this.closed) {
            this.setState(ConnectionState.CLOSED);
          } else {
            this.setState(ConnectionState.DISCONNECTED);
          }
          
          this.emit('close', event);
        };
      } catch (error) {
        this.clearConnectionTimeout();
        this.setState(ConnectionState.ERROR);
        reject(error);
      }
    });
  }

  /**
   * Send a message. If offline, queues the message for later.
   * @param message - The message to send
   * @returns Promise that resolves when sent (or queued)
   */
  async send(message: types.RealtimeSend): Promise<void> {
    const OPEN = 1; // WebSocket.OPEN
    
    if (this.ws && this.ws.readyState === OPEN) {
      this.ws.send(JSON.stringify(encode(message, "RealtimeSend")));
      return;
    }

    // Queue message for later if offline queue is enabled
    if (!this.config.enableOfflineQueue) {
      throw new Error('WebSocket is not connected');
    }

    return new Promise((resolve, reject) => {
      if (this.messageQueue.length >= (this.config.maxQueueSize || 1000)) {
        reject(new Error('Message queue full'));
        return;
      }

      this.messageQueue.push({
        data: message,
        timestamp: Date.now(),
        resolve,
        reject,
      });
    });
  }

  /**
   * Send a message immediately. Throws if not connected.
   * @param message - The message to send
   */
  sendSync(message: types.RealtimeSend): void {
    const OPEN = 1; // WebSocket.OPEN
    if (!this.ws || this.ws.readyState !== OPEN) {
      throw new Error('WebSocket is not connected');
    }
    this.ws.send(JSON.stringify(encode(message, "RealtimeSend")));
  }

  /**
   * Get the number of messages in the offline queue.
   */
  getQueueSize(): number {
    return this.messageQueue.length;
  }

  /**
   * Clear all messages from the offline queue.
   * @param rejectPending - If true, reject pending promises (default: false)
   */
  clearQueue(rejectPending: boolean = false): void {
    if (rejectPending) {
      const error = new Error('Queue cleared');
      this.messageQueue.forEach(msg => msg.reject(error));
    }
    this.messageQueue = [];
  }

  private flushQueue(): void {
    const OPEN = 1; // WebSocket.OPEN
    if (!this.ws || this.ws.readyState !== OPEN) return;

    const now = Date.now();
    const ttl = this.config.queueMessageTTL || 300000;

    while (this.messageQueue.length > 0) {
      const msg = this.messageQueue[0];
      
      // Check if message expired
      if (now - msg.timestamp > ttl) {
        this.messageQueue.shift();
        msg.reject(new Error('Message expired in queue'));
        continue;
      }

      try {
        this.ws.send(JSON.stringify(encode(msg.data, "RealtimeSend")));
        this.messageQueue.shift();
        msg.resolve();
      } catch (error) {
        // Stop flushing on error, will retry on next connection
        break;
      }
    }
  }

  /**
   * Register a handler for incoming messages.
   * @param handler - Function to call when a message is received
   */
  onMessage(handler: (message: types.RealtimeReceive) => void): void {
    this.on('message', handler);
  }

  /**
   * Remove a message handler.
   * @param handler - The handler to remove
   */
  offMessage(handler: (message: types.RealtimeReceive) => void): void {
    this.off('message', handler);
  }

  /**
   * Register a handler for errors.
   * @param handler - Function to call when an error occurs
   */
  onError(handler: (error: Error) => void): void {
    this.on('error', handler);
  }

  /**
   * Register a handler for connection close.
   * @param handler - Function to call when connection closes
   */
  onClose(handler: (event: CloseEvent) => void): void {
    this.on('close', handler);
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

  private rejectAllQueuedMessages(error: Error): void {
    this.messageQueue.forEach(msg => msg.reject(error));
    this.messageQueue = [];
  }

  /**
   * Close the WebSocket connection.
   * @param rejectQueuedMessages - If true, reject all queued messages (default: false)
   */
  close(rejectQueuedMessages: boolean = false): void {
    this.closed = true;
    this.clearConnectionTimeout();

    if (rejectQueuedMessages) {
      this.rejectAllQueuedMessages(new Error('Connection closed'));
    }

    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.setState(ConnectionState.CLOSED);
  }

  /**
   * Check if the WebSocket is currently connected.
   */
  isConnected(): boolean {
    return this.state === ConnectionState.CONNECTED;
  }
}

