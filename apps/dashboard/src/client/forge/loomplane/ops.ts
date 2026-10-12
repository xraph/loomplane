import type { CodecRef } from './codec-runtime';
import { CODECS } from './codecs';

/**
 * Operation manifest.
 *
 * Generated. Entity identity was resolved in Go against the response schema, so
 * the runtime never has to guess which field identifies a record -- the class of
 * guess that, made wrong on a type carrying both an id and a tenant id, keys two
 * tenants' records to one cache entry.
 */

export interface OperationMeta {
  readonly method: string;
  readonly path: string;
  /** The entity this operation's cache contract is about. */
  readonly entity?: string;
  /**
   * The typename of the response document itself -- of its ELEMENTS when the
   * response is a bare array -- which is what indexes the entities table below.
   *
   * Not interchangeable with the entity field above. On a paginated read the
   * two differ: entity is 'Order' while rootType is 'PageOrder'. Normalizing
   * such a response against 'Order' would read Order's field edges against an
   * envelope's properties and descend into nothing.
   */
  readonly rootType?: string;
  /** Milliseconds the client considers this operation's result fresh. */
  readonly staleTime?: number;
  readonly provides: readonly string[];
  readonly invalidates: readonly string[];
  /**
   * Keys into securitySchemes below, so an AuthProvider can attach exactly
   * the credential this operation declared instead of blanketing every
   * request with every credential the document happens to carry.
   */
  readonly security?: readonly string[];
  /**
   * Schema id (a key into src/codecs.ts's CODECS table) that renames this
   * operation's JSON request body from its TypeScript shape to the wire shape.
   *
   * Present only when the endpoint's request body is application/json AND
   * resolves to a named component schema -- the same condition under which the
   * generated typed method sets RequestConfig.bodyCodec, because both come
   * from the same resolver.
   */
  readonly bodyCodec?: CodecRef;
  /** The same, for decoding a JSON response back into its TypeScript shape. */
  readonly responseCodec?: CodecRef;
}

/**
 * A row with no idField is a signpost, not a record: an envelope, or a hop
 * between two entities. It is walked for its fields and never stored.
 *
 * The property names below -- idField, and the KEYS of fields -- are the
 * client-side names this client's field naming produces, because that is what
 * the decoded payload actually carries. The VALUES of fields are typenames,
 * which are not field names and are never renamed.
 */
export interface EntityMeta {
  readonly idField?: string;
  readonly fields?: Readonly<Record<string, string>>;
}

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

export const ops = {
  'infrastructure.get': {
    method: 'GET',
    path: '/api/v1/infrastructure',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['InfrastructureStatus'],
  },
  'invitations.list': {
    method: 'GET',
    path: '/api/v1/invitations',
    entity: 'OrgInvitation',
    rootType: 'OrgInvitationList',
    provides: ['OrgInvitation:{id}', 'OrgInvitation[]'],
    invalidates: [],
    responseCodec: CODECS['OrgInvitationList'],
  },
  'invitations.create': {
    method: 'POST',
    path: '/api/v1/invitations',
    provides: [],
    invalidates: ['OrgInvitation[]'],
    bodyCodec: CODECS['CreateInvitationInputBody'],
    responseCodec: CODECS['CreatedInvitation'],
  },
  'members.list': {
    method: 'GET',
    path: '/api/v1/members',
    entity: 'OrgMember',
    rootType: 'OrgMemberList',
    provides: ['OrgMember:{id}', 'OrgMember[]'],
    invalidates: [],
    responseCodec: CODECS['OrgMemberList'],
  },
  'projects.list': {
    method: 'GET',
    path: '/api/v1/projects',
    entity: 'Project',
    rootType: 'ProjectList',
    provides: ['Project:{id}', 'Project[]'],
    invalidates: [],
    responseCodec: CODECS['ProjectList'],
  },
  'projects.create': {
    method: 'POST',
    path: '/api/v1/projects',
    entity: 'Project',
    rootType: 'Project',
    provides: ['Project:{id}'],
    invalidates: ['Project[]'],
    bodyCodec: CODECS['CreateProjectInputBody'],
    responseCodec: CODECS['Project'],
  },
  'projects.get': {
    method: 'GET',
    path: '/api/v1/projects/{id}',
    entity: 'Project',
    rootType: 'Project',
    provides: ['Project:{id}'],
    invalidates: [],
    responseCodec: CODECS['Project'],
  },
  'projects.update': {
    method: 'PATCH',
    path: '/api/v1/projects/{id}',
    entity: 'Project',
    rootType: 'Project',
    provides: ['Project:{id}'],
    invalidates: ['Project[]'],
    bodyCodec: CODECS['UpdateProjectInputBody'],
    responseCodec: CODECS['Project'],
  },
  'projects.delete': {
    method: 'DELETE',
    path: '/api/v1/projects/{id}',
    entity: 'Project',
    provides: [],
    invalidates: ['Project[]'],
  },
  'environments.list': {
    method: 'GET',
    path: '/api/v1/projects/{id}/environments',
    entity: 'Environment',
    rootType: 'EnvironmentList',
    provides: ['Environment:{id}', 'Environment[]'],
    invalidates: [],
    responseCodec: CODECS['EnvironmentList'],
  },
  'publicConfig.get': {
    method: 'GET',
    path: '/api/v1/public-config',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['PublicConfig'],
  },
  'session.get': {
    method: 'GET',
    path: '/api/v1/session',
    provides: [],
    invalidates: [],
    responseCodec: CODECS['Session'],
  },
} as const satisfies Record<string, OperationMeta>;

export const entities = {
  'Environment': { idField: 'id' },
  'EnvironmentList': { fields: { 'environments': 'Environment' } },
  'OrgInvitation': { idField: 'id' },
  'OrgInvitationList': { fields: { 'invitations': 'OrgInvitation' } },
  'OrgMember': { idField: 'id' },
  'OrgMemberList': { fields: { 'members': 'OrgMember' } },
  'Project': { idField: 'id' },
  'ProjectList': { fields: { 'projects': 'Project' } },
} as const satisfies Record<string, EntityMeta>;

export const streams = [
] as const;
