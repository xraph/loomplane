import type { OperationMeta } from '../ops';
import { codec_SettingsInputBody } from '../codecs/SettingsInputBody';
import { codec_SettingsView } from '../codecs/SettingsView';

export const op_platformCloudSettings_set = {
  method: 'PUT',
  path: '/api/v1/platform/cloud/settings',
  provides: [],
  invalidates: [],
  bodyCodec: codec_SettingsInputBody,
  responseCodec: codec_SettingsView,
} as const satisfies OperationMeta;
