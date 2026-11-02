import { mutation } from '@forge-go/client-core';
import { op_aiSettings_putProviderKey } from '../ops/aiSettings.putProviderKey';
import type { ProviderKey } from '../types';

export const useAiSettingsPutProviderKey = /*#__PURE__*/ mutation<ProviderKey, ProviderKey>(op_aiSettings_putProviderKey);
