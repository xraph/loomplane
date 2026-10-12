// Generated error classes

export class APIError extends Error {
  constructor(
    public statusCode: number,
    message: string,
    public code?: string,
    public details?: Record<string, any>
  ) {
    super(message);
    this.name = 'APIError';
    Object.setPrototypeOf(this, APIError.prototype);
  }

  toJSON() {
    return {
      name: this.name,
      statusCode: this.statusCode,
      message: this.message,
      code: this.code,
      details: this.details,
    };
  }
}

export class ValidationError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(400, message || 'Validation failed', code, details);
    this.name = 'ValidationError';
    Object.setPrototypeOf(this, ValidationError.prototype);
  }
}

export class UnauthorizedError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(401, message || 'Authentication required', code, details);
    this.name = 'UnauthorizedError';
    Object.setPrototypeOf(this, UnauthorizedError.prototype);
  }
}

export class ForbiddenError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(403, message || 'Access forbidden', code, details);
    this.name = 'ForbiddenError';
    Object.setPrototypeOf(this, ForbiddenError.prototype);
  }
}

export class NotFoundError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(404, message || 'Resource not found', code, details);
    this.name = 'NotFoundError';
    Object.setPrototypeOf(this, NotFoundError.prototype);
  }
}

export class MethodNotAllowedError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(405, message || 'Method not allowed', code, details);
    this.name = 'MethodNotAllowedError';
    Object.setPrototypeOf(this, MethodNotAllowedError.prototype);
  }
}

export class ConflictError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(409, message || 'Resource conflict', code, details);
    this.name = 'ConflictError';
    Object.setPrototypeOf(this, ConflictError.prototype);
  }
}

export class TooManyRequestsError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(429, message || 'Too many requests', code, details);
    this.name = 'TooManyRequestsError';
    Object.setPrototypeOf(this, TooManyRequestsError.prototype);
  }
}

export class ServerError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(500, message || 'Internal server error', code, details);
    this.name = 'ServerError';
    Object.setPrototypeOf(this, ServerError.prototype);
  }
}

export class BadGatewayError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(502, message || 'Bad gateway', code, details);
    this.name = 'BadGatewayError';
    Object.setPrototypeOf(this, BadGatewayError.prototype);
  }
}

export class ServiceUnavailableError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(503, message || 'Service unavailable', code, details);
    this.name = 'ServiceUnavailableError';
    Object.setPrototypeOf(this, ServiceUnavailableError.prototype);
  }
}

export class GatewayTimeoutError extends APIError {
  constructor(message?: string, code?: string, details?: Record<string, any>) {
    super(504, message || 'Gateway timeout', code, details);
    this.name = 'GatewayTimeoutError';
    Object.setPrototypeOf(this, GatewayTimeoutError.prototype);
  }
}

/**
 * Creates an appropriate error class based on status code
 */
export function createError(
  statusCode: number,
  message: string,
  code?: string,
  details?: Record<string, any>
): APIError {
  switch (statusCode) {
    case 400:
      return new ValidationError(message, code, details);
    case 401:
      return new UnauthorizedError(message, code, details);
    case 403:
      return new ForbiddenError(message, code, details);
    case 404:
      return new NotFoundError(message, code, details);
    case 405:
      return new MethodNotAllowedError(message, code, details);
    case 409:
      return new ConflictError(message, code, details);
    case 429:
      return new TooManyRequestsError(message, code, details);
    case 500:
      return new ServerError(message, code, details);
    case 502:
      return new BadGatewayError(message, code, details);
    case 503:
      return new ServiceUnavailableError(message, code, details);
    case 504:
      return new GatewayTimeoutError(message, code, details);
    default:
      if (statusCode >= 400 && statusCode < 500) {
        return new APIError(statusCode, message, code, details);
      }
      if (statusCode >= 500) {
        return new ServerError(message, code, details);
      }
      return new APIError(statusCode, message, code, details);
  }
}

// Type guard functions
export function isAPIError(error: any): error is APIError {
  return error instanceof APIError;
}

export function isClientError(error: any): boolean {
  return isAPIError(error) && error.statusCode >= 400 && error.statusCode < 500;
}

export function isServerError(error: any): boolean {
  return isAPIError(error) && error.statusCode >= 500;
}
