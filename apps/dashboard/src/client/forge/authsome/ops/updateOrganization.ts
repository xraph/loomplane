import type { OperationMeta } from '../ops';
import { codec_Organization } from '../codecs/Organization';
import { codec_UpdateOrgRequest } from '../codecs/UpdateOrgRequest';

export const op_updateOrganization = {
  method: 'PATCH',
  path: '/authsome/v1/orgs/{orgId}',
  entity: 'Organization',
  rootType: 'Organization',
  provides: ['Organization:{id}'],
  invalidates: ['Organization[]'],
  security: ['bearerAuth'],
  bodyCodec: codec_UpdateOrgRequest,
  responseCodec: codec_Organization,
} as const satisfies OperationMeta;
