import { mutation } from '@forge-go/client-core';
import { op_dbGateways_pauseRoute } from '../ops/dbGateways.pauseRoute';
import type { RouteSummary } from '../types';

export const useDbGatewaysPauseRoute = /*#__PURE__*/ mutation<RouteSummary, RouteSummary>(op_dbGateways_pauseRoute);
