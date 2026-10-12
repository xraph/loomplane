import type { OperationMeta } from '../ops';
import { codec_CreateOrgRequest } from '../codecs/CreateOrgRequest';
import { codec_Organization } from '../codecs/Organization';

export const op_createOrganization = {
  method: 'POST',
  path: '/authsome/v1/orgs',
  entity: 'Organization',
  rootType: 'Organization',
  provides: ['Organization:{id}'],
  invalidates: ['Organization[]'],
  security: ['bearerAuth'],
  bodyCodec: codec_CreateOrgRequest,
  responseCodec: codec_Organization,
} as const satisfies OperationMeta;
