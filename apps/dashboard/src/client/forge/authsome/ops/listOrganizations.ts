import type { OperationMeta } from '../ops';
import { codec_OrgListResponse } from '../codecs/OrgListResponse';

export const op_listOrganizations = {
  method: 'GET',
  path: '/authsome/v1/orgs',
  entity: 'Organization',
  rootType: 'OrgListResponse',
  provides: ['Organization:{id}', 'Organization[]'],
  invalidates: [],
  security: ['bearerAuth'],
  responseCodec: codec_OrgListResponse,
} as const satisfies OperationMeta;
