import { query } from '@forge-go/client-core';
import { op_agentReports_get } from '../ops/agentReports.get';
import type { Report } from '../types';

export const useAgentReportsGet = /*#__PURE__*/ query<Report>(op_agentReports_get);
