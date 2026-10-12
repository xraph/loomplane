/**
 * Every security scheme the document declares, described once.
 *
 * Normalized rather than inlined per operation, for the same reason
 * `entities` is its own table: repeating four fields across two hundred
 * operations is bundle weight for data that never varies. An operation
 * carries the keys; an AuthProvider looks them up here.
 */
export const securitySchemes = {
  'session': { type: 'http', scheme: 'bearer' },
  'bearerAuth': { type: 'http', scheme: 'bearer' },
  'session-cookie': { type: 'apiKey', in: 'cookie', name: 'auth_token' },
} as const;
