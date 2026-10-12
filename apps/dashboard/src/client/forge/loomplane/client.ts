import { HTTPClient } from './fetch';
import type { RequestConfig } from './fetch';
import type { ClientConfig, AuthConfig } from './types';
import { createError } from './errors';

export class Client {
  protected httpClient: HTTPClient;
  private auth?: AuthConfig | undefined;

  constructor(config: ClientConfig) {
    this.auth = config.auth;
    this.httpClient = new HTTPClient(
      config.baseURL,
      config.timeout || 30000
    );

    // Setup auth headers
    if (this.auth?.bearerToken) {
      this.httpClient.setDefaultHeader('Authorization', `Bearer ${this.auth.bearerToken}`);
    }
    if (this.auth?.apiKey) {
      this.httpClient.setDefaultHeader('X-API-Key', this.auth.apiKey);
    }
    if (this.auth?.customHeaders) {
      for (const [key, value] of Object.entries(this.auth.customHeaders)) {
        this.httpClient.setDefaultHeader(key, value);
      }
    }
  }

  protected async request<T>(config: RequestConfig): Promise<T> {
    try {
      return await this.httpClient.request<T>(config);
    } catch (error: any) {
      // Transform errors into typed error classes
      if (error.statusCode) {
        throw createError(error.statusCode, error.message, error.code, error.details);
      }
      throw error;
    }
  }
}
