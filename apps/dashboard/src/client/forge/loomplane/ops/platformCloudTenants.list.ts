import type { OperationMeta } from '../ops';
import { codec_PlatformcloudTenantList } from '../codecs/PlatformcloudTenantList';

export const op_platformCloudTenants_list = {
  method: 'GET',
  path: '/api/v1/platform/cloud/tenants',
  provides: [],
  invalidates: [],
  responseCodec: codec_PlatformcloudTenantList,
} as const satisfies OperationMeta;
