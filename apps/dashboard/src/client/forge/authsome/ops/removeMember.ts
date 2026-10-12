import type { OperationMeta } from '../ops';
import { codec_OrganizationStatusResponse } from '../codecs/OrganizationStatusResponse';

export const op_removeMember = {
  method: 'DELETE',
  path: '/authsome/v1/orgs/{orgId}/members/{memberId}',
  provides: [],
  invalidates: [],
  security: ['bearerAuth'],
  responseCodec: codec_OrganizationStatusResponse,
} as const satisfies OperationMeta;
