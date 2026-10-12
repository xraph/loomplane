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
  'switchOrg': {
    method: 'POST',
    path: '/authsome/v1/me/switch-org',
    provides: [],
    invalidates: [],
    security: ['session', 'session-cookie'],
    bodyCodec: CODECS['SwitchOrgRequest'],
    responseCodec: CODECS['SwitchOrgResponse'],
  },
  'listOrganizations': {
    method: 'GET',
    path: '/authsome/v1/orgs',
    entity: 'Organization',
    rootType: 'OrgListResponse',
    provides: ['Organization:{id}', 'Organization[]'],
    invalidates: [],
    security: ['bearerAuth'],
    responseCodec: CODECS['OrgListResponse'],
  },
  'createOrganization': {
    method: 'POST',
    path: '/authsome/v1/orgs',
    entity: 'Organization',
    rootType: 'Organization',
    provides: ['Organization:{id}'],
    invalidates: ['Organization[]'],
    security: ['bearerAuth'],
    bodyCodec: CODECS['CreateOrgRequest'],
    responseCodec: CODECS['Organization'],
  },
  'checkOrgSlug': {
    method: 'GET',
    path: '/authsome/v1/orgs/check-slug',
    provides: [],
    invalidates: [],
    security: ['bearerAuth'],
    responseCodec: CODECS['SlugAvailableResponse'],
  },
  'acceptInvitation': {
    method: 'POST',
    path: '/authsome/v1/orgs/invitations/accept',
    entity: 'Member',
    rootType: 'Member',
    provides: ['Member:{id}'],
    invalidates: ['Member[]'],
    security: ['bearerAuth'],
    bodyCodec: CODECS['AcceptInvitationRequest'],
    responseCodec: CODECS['Member'],
  },
  'declineInvitation': {
    method: 'POST',
    path: '/authsome/v1/orgs/invitations/decline',
    provides: [],
    invalidates: [],
    security: ['bearerAuth'],
    bodyCodec: CODECS['DeclineInvitationRequest'],
    responseCodec: CODECS['OrganizationStatusResponse'],
  },
  'getOrganization': {
    method: 'GET',
    path: '/authsome/v1/orgs/{orgId}',
    entity: 'Organization',
    rootType: 'Organization',
    provides: ['Organization:{id}'],
    invalidates: [],
    security: ['bearerAuth'],
    responseCodec: CODECS['Organization'],
  },
  'updateOrganization': {
    method: 'PATCH',
    path: '/authsome/v1/orgs/{orgId}',
    entity: 'Organization',
    rootType: 'Organization',
    provides: ['Organization:{id}'],
    invalidates: ['Organization[]'],
    security: ['bearerAuth'],
    bodyCodec: CODECS['UpdateOrgRequest'],
    responseCodec: CODECS['Organization'],
  },
  'deleteOrganization': {
    method: 'DELETE',
    path: '/authsome/v1/orgs/{orgId}',
    provides: [],
    invalidates: [],
    security: ['bearerAuth'],
    responseCodec: CODECS['OrganizationStatusResponse'],
  },
  'listInvitations': {
    method: 'GET',
    path: '/authsome/v1/orgs/{orgId}/invitations',
    entity: 'Invitation',
    rootType: 'InvitationListResponse',
    provides: ['Invitation:{id}', 'Invitation[]'],
    invalidates: [],
    security: ['bearerAuth'],
    responseCodec: CODECS['InvitationListResponse'],
  },
  'createInvitation': {
    method: 'POST',
    path: '/authsome/v1/orgs/{orgId}/invitations',
    entity: 'Invitation',
    rootType: 'Invitation',
    provides: ['Invitation:{id}'],
    invalidates: ['Invitation[]'],
    security: ['bearerAuth'],
    bodyCodec: CODECS['CreateInvitationRequest'],
    responseCodec: CODECS['Invitation'],
  },
  'listMembers': {
    method: 'GET',
    path: '/authsome/v1/orgs/{orgId}/members',
    entity: 'Member',
    rootType: 'MemberListResponse',
    provides: ['Member:{id}', 'Member[]'],
    invalidates: [],
    security: ['bearerAuth'],
    responseCodec: CODECS['MemberListResponse'],
  },
  'addMember': {
    method: 'POST',
    path: '/authsome/v1/orgs/{orgId}/members',
    entity: 'Member',
    rootType: 'Member',
    provides: ['Member:{id}'],
    invalidates: ['Member[]'],
    security: ['bearerAuth'],
    bodyCodec: CODECS['AddMemberRequest'],
    responseCodec: CODECS['Member'],
  },
  'updateMember': {
    method: 'PATCH',
    path: '/authsome/v1/orgs/{orgId}/members/{memberId}',
    entity: 'Member',
    rootType: 'Member',
    provides: ['Member:{id}'],
    invalidates: ['Member[]'],
    security: ['bearerAuth'],
    bodyCodec: CODECS['UpdateMemberRequest'],
    responseCodec: CODECS['Member'],
  },
  'removeMember': {
    method: 'DELETE',
    path: '/authsome/v1/orgs/{orgId}/members/{memberId}',
    provides: [],
    invalidates: [],
    security: ['bearerAuth'],
    responseCodec: CODECS['OrganizationStatusResponse'],
  },
} as const satisfies Record<string, OperationMeta>;

export const entities = {
  'Invitation': { idField: 'id' },
  'InvitationListResponse': { fields: { 'invitations': 'Invitation' } },
  'Member': { idField: 'id' },
  'MemberListResponse': { fields: { 'members': 'Member' } },
  'OrgListResponse': { fields: { 'organizations': 'Organization' } },
  'Organization': { idField: 'id' },
} as const satisfies Record<string, EntityMeta>;

export const streams = [
] as const;
