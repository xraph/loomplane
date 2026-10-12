import type { OperationMeta } from '../ops';
import { codec_OrganizationStatusResponse } from '../codecs/OrganizationStatusResponse';

export const op_deleteOrganization = {
  method: 'DELETE',
  path: '/authsome/v1/orgs/{orgId}',
  provides: [],
  invalidates: [],
  security: ['bearerAuth'],
  responseCodec: codec_OrganizationStatusResponse,
} as const satisfies OperationMeta;
