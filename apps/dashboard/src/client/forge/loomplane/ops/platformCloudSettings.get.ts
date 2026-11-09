import type { OperationMeta } from '../ops';
import { codec_SettingsView } from '../codecs/SettingsView';

export const op_platformCloudSettings_get = {
  method: 'GET',
  path: '/api/v1/platform/cloud/settings',
  provides: [],
  invalidates: [],
  responseCodec: codec_SettingsView,
} as const satisfies OperationMeta;
