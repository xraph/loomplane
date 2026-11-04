import { query } from '@forge-go/client-core';
import { op_agentReports_list } from '../ops/agentReports.list';
import type { ReportList } from '../types';

export const useAgentReportsList = /*#__PURE__*/ query<ReportList>(op_agentReports_list);
