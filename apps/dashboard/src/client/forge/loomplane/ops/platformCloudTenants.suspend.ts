import type { OperationMeta } from '../ops';
import { codec_PlatformcloudTenant } from '../codecs/PlatformcloudTenant';

export const op_platformCloudTenants_suspend = {
  method: 'POST',
  path: '/api/v1/platform/cloud/tenants/{org}/suspend',
  provides: [],
  invalidates: [],
  responseCodec: codec_PlatformcloudTenant,
} as const satisfies OperationMeta;
