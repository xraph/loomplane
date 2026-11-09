import { query } from '@forge-go/client-core';
import { op_agentWatcher_get } from '../ops/agentWatcher.get';
import type { AgentwatchStatus } from '../types';

export const useAgentWatcherGet = /*#__PURE__*/ query<AgentwatchStatus>(op_agentWatcher_get);
