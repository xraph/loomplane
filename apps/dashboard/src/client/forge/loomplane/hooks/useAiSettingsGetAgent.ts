import { query } from '@forge-go/client-core';
import { op_aiSettings_getAgent } from '../ops/aiSettings.getAgent';
import type { AgentView } from '../types';

export const useAiSettingsGetAgent = /*#__PURE__*/ query<AgentView>(op_aiSettings_getAgent);
