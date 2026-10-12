import type { OperationMeta } from '../ops';
import { codec_Organization } from '../codecs/Organization';

export const op_getOrganization = {
  method: 'GET',
  path: '/authsome/v1/orgs/{orgId}',
  entity: 'Organization',
  rootType: 'Organization',
  provides: ['Organization:{id}'],
  invalidates: [],
  security: ['bearerAuth'],
  responseCodec: codec_Organization,
} as const satisfies OperationMeta;
