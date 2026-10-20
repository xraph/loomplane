import type { OperationMeta } from '../ops';
import { codec_Preferences } from '../codecs/Preferences';

export const op_notifications_preferences_get = {
  method: 'GET',
  path: '/api/v1/notifications/preferences',
  provides: [],
  invalidates: [],
  responseCodec: codec_Preferences,
} as const satisfies OperationMeta;
