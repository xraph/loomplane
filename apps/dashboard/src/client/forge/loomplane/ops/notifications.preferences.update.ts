import type { OperationMeta } from '../ops';
import { codec_PreferenceInputBody } from '../codecs/PreferenceInputBody';
import { codec_Preferences } from '../codecs/Preferences';

export const op_notifications_preferences_update = {
  method: 'PUT',
  path: '/api/v1/notifications/preferences',
  provides: [],
  invalidates: [],
  bodyCodec: codec_PreferenceInputBody,
  responseCodec: codec_Preferences,
} as const satisfies OperationMeta;
