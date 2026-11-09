import { query } from '@forge-go/client-core';
import { op_aiSettings_get } from '../ops/aiSettings.get';
import type { View } from '../types';

export const useAiSettingsGet = /*#__PURE__*/ query<View>(op_aiSettings_get);
