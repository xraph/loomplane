import { query } from '@forge-go/client-core';
import { op_platformCloudSettings_get } from '../ops/platformCloudSettings.get';
import type { SettingsView } from '../types';

export const usePlatformCloudSettingsGet = /*#__PURE__*/ query<SettingsView>(op_platformCloudSettings_get);
