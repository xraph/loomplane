import { mutation } from '@forge-go/client-core';
import { op_dbGateways_resumeRoute } from '../ops/dbGateways.resumeRoute';
import type { RouteSummary } from '../types';

export const useDbGatewaysResumeRoute = /*#__PURE__*/ mutation<RouteSummary, RouteSummary>(op_dbGateways_resumeRoute);
