import type { OperationMeta } from '../ops';
import { codec_PlatformcloudTenant } from '../codecs/PlatformcloudTenant';
import { codec_PlatformcloudTenantInputBody } from '../codecs/PlatformcloudTenantInputBody';

export const op_platformCloudTenants_set = {
  method: 'PUT',
  path: '/api/v1/platform/cloud/tenants/{org}',
  provides: [],
  invalidates: [],
  bodyCodec: codec_PlatformcloudTenantInputBody,
  responseCodec: codec_PlatformcloudTenant,
} as const satisfies OperationMeta;
