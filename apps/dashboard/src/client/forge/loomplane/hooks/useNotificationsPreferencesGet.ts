import { query } from '@forge-go/client-core';
import { op_notifications_preferences_get } from '../ops/notifications.preferences.get';
import type { Preferences } from '../types';

export const useNotificationsPreferencesGet = /*#__PURE__*/ query<Preferences>(op_notifications_preferences_get);
