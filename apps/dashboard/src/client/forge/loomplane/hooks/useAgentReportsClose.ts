import { mutation } from '@forge-go/client-core';
import { op_agentReports_close } from '../ops/agentReports.close';
import type { Report } from '../types';

export const useAgentReportsClose = /*#__PURE__*/ mutation<Report, Report>(op_agentReports_close);
