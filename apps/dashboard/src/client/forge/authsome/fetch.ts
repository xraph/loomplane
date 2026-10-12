// Base HTTP client using native fetch
import { decode, encode } from './codec-runtime';
import type { CodecRef } from './codec-runtime';

/** Error thrown for non-2xx responses. */
export class HTTPError extends Error {
  readonly statusCode: number;
  readonly code: string;
  readonly details: unknown;

  constructor(statusCode: number, message: string, code: string, details: unknown) {
    super(message);
    this.name = 'HTTPError';
    this.statusCode = statusCode;
    this.code = code;
    this.details = details;
  }
}

/**
 * A request interceptor's onRequest(config) may return a REPLACEMENT
 * object instead of a mutated/spread copy of `config` -- both are legal
 * readings of `RequestConfig | Promise<RequestConfig>`. A replacement that
 * only lists the fields it cares about (e.g. `return { method, url, body };`)
 * silently drops every field it didn't name -- bodyCodec/responseCodec
 * included, alongside the pre-existing allowEmptyBody/signal/retry. Before
 * bodyCodec/responseCodec existed, that hazard meant a missing timeout
 * signal or a wrongly-collapsed empty body; now it also means a JSON body
 * silently ships wire-cased and unrenamed, or a JSON response is silently
 * left un-decoded, with no error anywhere. Always spread the incoming
 * config -- `return { ...config, headers: { ...config.headers, ... } };` --
 * rather than building a replacement object from scratch.
 */
export interface RequestConfig {
  method: string;
  url: string;
  headers?: Record<string, string> | undefined;
  body?: any | undefined;
  signal?: AbortSignal | undefined;
  retry?: RetryConfig | undefined;
  // The fetch credentials mode, forwarded from `RestRequestConfig` in
  // packages/client-core/src/transport.ts. Declared here for the same
  // reason bodyCodec/responseCodec are below: executeRequest builds the
  // RequestInit it hands to fetch() only from the fields this config
  // carries, so a config that omits this one is a cross-origin request
  // that silently never sends its session cookie. Left unset by default,
  // which is exactly fetch()'s own default (same-origin) and today's
  // generated behaviour; 'include' is the opt-in for a cross-origin
  // session.
  credentials?: RequestCredentials;
  // Set by the generated method when its declared return type has a
  // no-content 2xx response (i.e. includes `void`). Only then does an
  // empty response body mean "there is legitimately nothing here" —
  // executeRequest cannot infer this from the bytes alone, since a
  // genuinely empty text/plain or binary body is valid data for an
  // endpoint that never declared a no-content response.
  allowEmptyBody?: boolean;
  // Schema id (a key into src/codecs.ts's CODECS table) used to rename
  // this request's JSON body from its TypeScript (camelCase) shape to
  // the wire shape before serialisation. Set by the generated method
  // only when the endpoint's request body is application/json AND
  // resolves to a named component schema (see rest.go's
  // requestBodyCodecRef) -- there is no codec-table entry for an inline
  // schema to reference. A FormData/Blob/string/stream/typed-array body
  // ignores this field entirely: executeRequest only calls encode() in
  // the branch that already decided the body is JSON-serialisable.
  bodyCodec?: CodecRef;
  // Same idea, for decoding a JSON response back into its TypeScript
  // shape. Only applied inside the JSON content-type branch below
  // (isJSONMediaType) -- a void/Blob/text response is never walked by
  // decode().
  responseCodec?: CodecRef;
}

export interface RetryConfig {
  maxAttempts?: number;
  delay?: number;
  maxDelay?: number;
  retryableStatusCodes?: number[];
}

// Combines two abort signals so that either aborting the caller's
// signal or the request timeout aborts the request. Falls back to
// manual forwarding on runtimes without AbortSignal.any. The caller
// must call dispose() once the request settles so fallback listeners
// don't accumulate on a reused, long-lived AbortController.
const combineSignals = (a: AbortSignal, b: AbortSignal): { signal: AbortSignal; dispose: () => void } => {
  const anyFn = (AbortSignal as any).any;
  if (typeof anyFn === 'function') {
    return { signal: anyFn.call(AbortSignal, [a, b]), dispose: () => {} };
  }
  // Manual fallback: forward whichever aborts first.
  const merged = new AbortController();
  const cleanups: Array<() => void> = [];
  const dispose = () => {
    for (const c of cleanups) c();
    cleanups.length = 0;
  };
  const forwardAbort = (source: AbortSignal) => {
    if (source.aborted) {
      merged.abort((source as any).reason);
      return;
    }
    const onAbort = () => {
      merged.abort((source as any).reason);
      dispose();
    };
    source.addEventListener('abort', onAbort);
    cleanups.push(() => source.removeEventListener('abort', onAbort));
  };
  forwardAbort(a);
  forwardAbort(b);
  return { signal: merged.signal, dispose };
};

const DEFAULT_RETRY_CONFIG: RetryConfig = {
  maxAttempts: 3,
  delay: 1000,
  maxDelay: 30000,
  retryableStatusCodes: [408, 429, 500, 502, 503, 504],
};

// The content-type rule every Forge client decodes by, and the one the
// generated methods are typed with. The essence is the type before any
// parameters, trimmed and lower-cased. JSON is application/json, text/json or
// any +json type (application/problem+json, application/vnd.api+json), and
// never a substring match: application/jsonl is text, not one JSON value.
// Text is text/*, any +xml or +yaml type, or one of TEXT_APPLICATION_TYPES.
// Everything else is bytes.
function mediaEssence(contentType: string | null): string {
  const value = contentType ?? '';
  const semicolon = value.indexOf(';');
  return (semicolon === -1 ? value : value.slice(0, semicolon)).trim().toLowerCase();
}

function isJSONMediaType(essence: string): boolean {
  return essence === 'application/json' || essence === 'text/json' || essence.endsWith('+json');
}

const TEXT_APPLICATION_TYPES: ReadonlySet<string> = new Set([
  'application/ecmascript',
  'application/graphql',
  'application/javascript',
  'application/jsonl',
  'application/sql',
  'application/x-javascript',
  'application/x-ndjson',
  'application/x-www-form-urlencoded',
  'application/x-yaml',
  'application/xml',
  'application/yaml',
]);

function isTextMediaType(essence: string): boolean {
  return essence.startsWith('text/') ||
    essence.endsWith('+xml') ||
    essence.endsWith('+yaml') ||
    TEXT_APPLICATION_TYPES.has(essence);
}

export class HTTPClient {
  private baseURL: string;
  private defaultHeaders: Record<string, string>;
  private timeout: number;

  constructor(baseURL: string, timeout: number = 30000) {
    this.baseURL = baseURL;
    this.timeout = timeout;
    // No default Content-Type here: unconditionally forcing
    // 'application/json' on every request — including ones with a
    // FormData/Blob/string body, or no body at all — is wrong. A JSON
    // body gets 'application/json' dynamically in executeRequest, only
    // when nothing has already set a Content-Type. A caller that wants a
    // different default for every request can still call
    // setDefaultHeader('Content-Type', ...) explicitly.
    this.defaultHeaders = {};
  }

  setDefaultHeader(key: string, value: string): void {
    this.defaultHeaders[key] = value;
  }

  async request<T>(config: RequestConfig): Promise<T> {
    const retryConfig = { ...DEFAULT_RETRY_CONFIG, ...config.retry };

    // A ReadableStream request body is one-shot: once it has been read by
    // one fetch() attempt, it is disturbed and cannot be re-sent by a
    // second attempt (unlike FormData/Blob/string, which are all
    // re-readable). Retrying it anyway would either throw a confusing
    // 'body stream already read' TypeError from fetch() itself — which
    // shouldRetry would then classify as a retryable network error and
    // retry again, burning every remaining attempt on that same error —
    // or, on runtimes tolerant of a disturbed stream, silently send an
    // empty body. Capping maxAttempts to 1 up front reuses the existing
    // attempt-loop and shouldRetry logic unchanged: attempt 0 is the only
    // attempt, so any failure surfaces immediately and loudly instead of
    // being retried at all.
    // isStreamBody uses Object.prototype.toString rather than `instanceof`
    // because instanceof is realm-bound: a stream created in an iframe, by
    // a polyfill, or by a bundler-substituted global is NOT an instance of
    // this realm's ReadableStream, so instanceof would miss it — sending an
    // empty body AND retrying it. Symbol.toStringTag is per-object, so this
    // check holds across realms.
    const isStreamBody = Object.prototype.toString.call(config.body) === '[object ReadableStream]';

    // The cap tests 'is a stream at all', not `.locked`. A stream is NOT
    // locked before the first attempt — fetch() locks it while reading — so
    // testing `.locked` here would be false every time and the cap would
    // never apply. That does leave the cap pessimistic: a failure that
    // never touched the body (DNS failure, connection refused, TLS
    // handshake) leaves the stream intact and would have been safely
    // retryable. Refining it needs a per-attempt disturbed check, and the
    // stream API exposes no `disturbed` getter to write one against.
    // Warn when the caller explicitly asked for retries, so silently
    // getting a single attempt is at least discoverable.
    if (isStreamBody) {
      if ((config.retry?.maxAttempts ?? 0) > 1) {
        console.warn(
          '[client] request body is a ReadableStream, which cannot be re-sent; ' +
          'retries are disabled for this request (requested maxAttempts: ' +
          String(config.retry?.maxAttempts) + ').'
        );
      }
      retryConfig.maxAttempts = 1;
    }

    let lastError: Error | null = null;

    for (let attempt = 0; attempt < (retryConfig.maxAttempts || 1); attempt++) {
      try {
        return await this.executeRequest<T>(config, attempt);
      } catch (error) {
        lastError = error as Error;

        // Check if we should retry
        const shouldRetry = this.shouldRetry(error, attempt, retryConfig);
        if (!shouldRetry) {
          throw error;
        }

        // Exponential backoff
        const delay = Math.min(
          (retryConfig.delay || 1000) * Math.pow(2, attempt),
          retryConfig.maxDelay || 30000
        );

        // Wait out the backoff, but let the caller's own abort interrupt it
        // early instead of always sitting out the full delay — with
        // production defaults (1s/2s/4s) an unabortable wait means a caller
        // who aborted 20ms in still waits out the rest of the delay. This
        // races against config.signal (the caller's own signal), not the
        // per-attempt timeout controller created inside executeRequest: the
        // timeout governs a single request attempt, not the gap between
        // attempts.
        await new Promise<void>((resolve, reject) => {
          const signal = config.signal;
          let timer: ReturnType<typeof setTimeout>;
          const onAbort = () => {
            clearTimeout(timer);
            reject((signal as any)?.reason ?? new DOMException('Aborted', 'AbortError'));
          };
          if (signal) {
            if (signal.aborted) {
              reject((signal as any).reason ?? new DOMException('Aborted', 'AbortError'));
              return;
            }
            // Removed explicitly when the timer wins (below), not left to
            // { once: true } alone, so it doesn't accumulate on a long-lived
            // caller signal reused across many non-aborted retry sequences.
            signal.addEventListener('abort', onAbort, { once: true });
          }
          timer = setTimeout(() => {
            if (signal) signal.removeEventListener('abort', onAbort);
            resolve();
          }, delay);
        });
      }
    }

    throw lastError || new Error('Request failed after retries');
  }

  private async executeRequest<T>(config: RequestConfig, attempt: number): Promise<T> {
    // A bare `{ ...config }` only copies the top-level fields: nested
    // objects like headers and retry stay aliased to the caller's config
    // (requestConfig.headers === config.headers). A request interceptor
    // that mutates config.headers in place — a legitimate reading of
    // onRequest(config): RequestConfig — would then compound its mutation
    // on every retry attempt, since each attempt re-spreads the same
    // already-mutated object. Copying these two nested objects keeps each
    // attempt's interceptor mutations scoped to that attempt. (`body` is
    // deliberately left aliased here too — a FormData/Blob/ReadableStream
    // value must stay the exact same object across attempts for a retry to
    // resend it at all; see the body-serialization block below for how
    // that aliased value is turned into an actual fetch() body.)
    let requestConfig: RequestConfig = {
      ...config,
      headers: config.headers ? { ...config.headers } : config.headers,
      retry: config.retry ? { ...config.retry } : config.retry,
    };

    // Build full URL
    const url = requestConfig.url.startsWith('http') 
      ? requestConfig.url 
      : this.baseURL + requestConfig.url;

    // Serialize the body based on its RUNTIME type, not any declared
    // TypeScript type (which is erased by the time this code runs).
    // FormData and Blob are native BodyInit values fetch already knows how
    // to send — passing them through untouched (rather than
    // JSON.stringify-ing them, which flattens either to the useless
    // literal string "{}") is required for a multipart upload or a raw
    // binary body to reach the server as anything but empty. A string
    // body (e.g. a text/plain endpoint) also passes through as-is. A
    // ReadableStream is included here for the same reason, even though
    // request() above already refuses to retry one — it must still be
    // sent untouched on its one and only attempt. Anything else is
    // assumed to be a JSON-serializable value.
    // Dispatch on Object.prototype.toString rather than `instanceof`:
    // instanceof is realm-bound, so a FormData/Blob/stream created in an
    // iframe, by a polyfill, or against a bundler-substituted global is not
    // an instance of THIS realm's constructor and would fall through to
    // JSON.stringify — sending "{}" with a JSON Content-Type. The
    // Symbol.toStringTag each of these types carries is per-object, so the
    // tag check holds across realms and needs no `typeof X !== 'undefined'`
    // guard for environments where the global is absent.
    let body: BodyInit | undefined;
    let isJSONBody = false;
    const bodyTag = Object.prototype.toString.call(requestConfig.body);
    if (requestConfig.body === undefined || requestConfig.body === null) {
      body = undefined;
    } else if (typeof requestConfig.body === 'string') {
      body = requestConfig.body;
    } else if (bodyTag === '[object FormData]') {
      body = requestConfig.body as FormData;
    } else if (bodyTag === '[object Blob]' || bodyTag === '[object File]') {
      body = requestConfig.body as Blob;
    } else if (bodyTag === '[object URLSearchParams]') {
      // Native BodyInit: fetch form-urlencodes it and sets the matching
      // Content-Type itself. JSON.stringify would have flattened it to
      // "{}" — URLSearchParams has no own enumerable properties.
      body = requestConfig.body as URLSearchParams;
    } else if (bodyTag === '[object ReadableStream]') {
      body = requestConfig.body as ReadableStream;
    } else if (bodyTag === '[object ArrayBuffer]' || ArrayBuffer.isView(requestConfig.body)) {
      // ArrayBuffer.isView covers every TypedArray and DataView, and is
      // realm-independent. Without this a Uint8Array would be stringified
      // index-by-index into {\"0\":1,\"1\":2,...} and an ArrayBuffer into "{}".
      body = requestConfig.body as BodyInit;
    } else {
      // Only a JSON-serialisable body ever reaches this branch -- every
      // native BodyInit shape (FormData, Blob/File, URLSearchParams,
      // ReadableStream, ArrayBuffer/TypedArray) and the plain-string case
      // above already returned/assigned before this point.
      // That is what
      // makes it safe to call encode() unconditionally here: it is only
      // ever given a value this branch has already established is meant
      // to be walked and JSON.stringify-ed, never one of the native
      // BodyInit values above. bodyCodec is only set by the generated
      // method when the endpoint's body resolves to a named schema (see
      // rest.go's requestBodyCodecRef); encode() is a no-op passthrough
      // for an undefined codec id.
      const encodedBody = requestConfig.bodyCodec !== undefined
        ? encode(requestConfig.body, requestConfig.bodyCodec)
        : requestConfig.body;
      body = JSON.stringify(encodedBody);
      isJSONBody = true;
    }

    // Merge headers. A JSON-serialized body gets a default
    // 'Content-Type: application/json' — but only when nothing has
    // already set a Content-Type (checked case-insensitively, since HTTP
    // header names are case-insensitive): an endpoint that declares an
    // explicit Content-Type header parameter, or a caller/interceptor
    // that sets one directly, must win. FormData, Blob, string, and
    // stream bodies never get this default at all — forcing a
    // Content-Type onto FormData specifically breaks the request, because
    // the runtime computes the multipart boundary only when it sets the
    // header itself; a manually-set 'multipart/form-data' has no boundary
    // and the server cannot parse the body.
    const headers: Record<string, string> = {
      ...this.defaultHeaders,
      ...requestConfig.headers,
    };
    if (isJSONBody && !Object.keys(headers).some((k) => k.toLowerCase() === 'content-type')) {
      headers['Content-Type'] = 'application/json';
    }

    // Create abort controller with timeout
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), this.timeout);

    // Combine the caller's signal with the timeout signal; using the
    // caller's alone would silently disable the timeout.
    const combined = requestConfig.signal
      ? combineSignals(requestConfig.signal, controller.signal)
      : { signal: controller.signal, dispose: () => {} };
    const signal = combined.signal;

    try {
      // Make fetch request
      let response = await fetch(url, {
        method: requestConfig.method,
        headers,
        ...(body === undefined ? {} : { body }),
        ...(requestConfig.credentials === undefined ? {} : { credentials: requestConfig.credentials }),
        signal,
      });

      // Handle non-OK responses
      if (!response.ok) {
        await this.handleErrorResponse(response);
      }

      // Parse response.
      //
      // Statuses that MUST NOT carry a body (RFC 9110 §15.3.5, §15.3.6):
      // reading them would be wasted work regardless of what the spec
      // declares, so `undefined` is returned unconditionally here — no
      // `allowEmptyBody` gate. (304 is deliberately not included: it fails
      // `response.ok`, so handleErrorResponse above already threw before
      // this line is ever reached — including it here would be dead code.)
      const noBodyStatus = response.status === 204 || response.status === 205;
      if (noBodyStatus) {
        return undefined as T;
      }

      // Every other response's body is read exactly once, as a Blob, and
      // the declared shape is derived from that Blob rather than calling
      // response.json()/.text()/.blob() directly — a Response body can only
      // be consumed once, so those must not be called more than once.
      const blob = await response.blob();

      // An empty body only means "there is nothing here" (-> undefined) when
      // the spec actually declared a no-content 2xx for this call
      // (requestConfig.allowEmptyBody, set by the generated method — see
      // generateMethodBody in rest.go). executeRequest is one generic
      // function shared by every endpoint, so it cannot otherwise tell an
      // endpoint that legitimately never returns a body (e.g. a bare 202
      // ack) apart from one that just happens to return an empty payload
      // this time (e.g. an empty text/plain string, or a zero-byte file
      // download) — collapsing both to `undefined` unconditionally would
      // silently corrupt the latter for any endpoint that never declared a
      // no-content response. Checking the actual byte count (rather than
      // trusting a Content-Length header) is deliberate even when the flag
      // is set: a real empty response is not guaranteed to carry
      // Content-Length at all (chunked transfer encoding omits it, and some
      // runtimes don't set it for a null body either).
      if (requestConfig.allowEmptyBody && blob.size === 0) {
        return undefined as T;
      }

      const essence = mediaEssence(response.headers.get('content-type'));
      if (isJSONMediaType(essence)) {
        // An empty body under a declared JSON type is a genuine error, not a
        // legitimate value (unless allowEmptyBody already returned above) —
        // JSON.parse('') throws, matching what response.json() would have
        // thrown before this method read the body itself.
        const parsed = JSON.parse(await blob.text());
        // responseCodec is only set by the generated method when this
        // response resolves to a named component schema (see rest.go's
        // responseCodecRef); decode() is a no-op passthrough for an
        // undefined codec id. This is the ONLY place decode() is called --
        // the 204/205 and allowEmptyBody early returns above, and the
        // text/* and Blob branches below, all return before reaching here,
        // so a void/text/Blob response is never walked by it.
        return requestConfig.responseCodec !== undefined
          ? (decode(parsed, requestConfig.responseCodec) as T)
          : parsed;
      }

      // A declared text return type (e.g. `string`) must be read as text —
      // an empty text/plain body is a legitimate empty string, not `void`,
      // unless allowEmptyBody already returned above. Anything else (e.g. a
      // declared `Blob` return type for a file download) is returned as the
      // Blob already read above, zero-byte or not — otherwise the declared
      // type would be a lie tsc cannot catch.
      if (isTextMediaType(essence)) {
        return await blob.text() as any;
      }

      return blob as any;
    } catch (error) {
      throw error;
    } finally {
      // The timeout and the abort-signal forwarding must stay live until
      // the response body has been fully read, not just until fetch()
      // resolves (headers only) — otherwise a server that sends headers then
      // stalls the body hangs forever, and (on the manual combineSignals
      // fallback) a caller abort during the body read never reaches the
      // merged controller once the forwarding listeners are gone. Tearing
      // both down in a finally that wraps the fetch call AND the whole
      // body-parsing block above covers every exit path — success, a
      // thrown network/parse error, and handleErrorResponse's throw —
      // exactly once. combined.dispose() is idempotent (an abort may already
      // have triggered it), so calling it again here is always safe.
      clearTimeout(timeoutId);
      combined.dispose();
    }
  }

  private async handleErrorResponse(response: Response): Promise<never> {
    let errorData: any = {};

    // An RFC 9457 error arrives as application/problem+json, so the JSON
    // test is the same one a success response gets.
    if (isJSONMediaType(mediaEssence(response.headers.get('content-type')))) {
      try {
        errorData = await response.json();
      } catch (e) {
        // Ignore JSON parse errors
      }
    }

    // Import error classes are assumed to be in errors.ts
    const message = errorData.message || errorData.error || response.statusText;
    const code = errorData.code || '';
    const details = errorData.details || errorData;

    throw new HTTPError(response.status, message, code, details);
  }

  private shouldRetry(error: any, attempt: number, config: RetryConfig): boolean {
    // Don't retry if we've exhausted attempts
    if (attempt >= (config.maxAttempts || 1) - 1) {
      return false;
    }

    // Don't retry if aborted
    if (error.name === 'AbortError') {
      return false;
    }

    // Retry on network errors
    if (error.name === 'TypeError' || error.message === 'Failed to fetch') {
      return true;
    }

    // Retry on retryable status codes
    if (error.statusCode) {
      return (config.retryableStatusCodes || []).includes(error.statusCode);
    }

    return false;
  }
}
