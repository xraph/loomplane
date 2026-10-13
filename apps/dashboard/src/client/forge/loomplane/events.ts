// Simple EventEmitter implementation for streaming clients

export type EventHandler = (...args: any[]) => void;

/**
 * Simple EventEmitter for managing event subscriptions.
 */
export class EventEmitter {
  private events: Map<string, Set<EventHandler>> = new Map();

  /**
   * Register an event handler.
   * @param event - Event name
   * @param handler - Handler function
   */
  on(event: string, handler: EventHandler): void {
    if (!this.events.has(event)) {
      this.events.set(event, new Set());
    }
    this.events.get(event)!.add(handler);
  }

  /**
   * Register a one-time event handler.
   * @param event - Event name
   * @param handler - Handler function
   */
  once(event: string, handler: EventHandler): void {
    const onceHandler = (...args: any[]) => {
      this.off(event, onceHandler);
      handler(...args);
    };
    this.on(event, onceHandler);
  }

  /**
   * Remove an event handler.
   * @param event - Event name
   * @param handler - Handler function to remove
   */
  off(event: string, handler: EventHandler): void {
    this.events.get(event)?.delete(handler);
  }

  /**
   * Remove all handlers for an event or all events.
   * @param event - Optional event name; if omitted, clears all events
   */
  removeAllListeners(event?: string): void {
    if (event) {
      this.events.delete(event);
    } else {
      this.events.clear();
    }
  }

  /**
   * Emit an event with arguments.
   * @param event - Event name
   * @param args - Arguments to pass to handlers
   */
  protected emit(event: string, ...args: any[]): void {
    const handlers = this.events.get(event);
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

  /**
   * Get the number of listeners for an event.
   * @param event - Event name
   * @returns Number of listeners
   */
  listenerCount(event: string): number {
    return this.events.get(event)?.size || 0;
  }

  /**
   * Get all registered event names.
   * @returns Array of event names
   */
  eventNames(): string[] {
    return Array.from(this.events.keys());
  }
}
