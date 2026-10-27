import { query } from '@forge-go/client-core';
import { op_dbGateways_list } from '../ops/dbGateways.list';
import type { GatewayList } from '../types';

export const useDbGatewaysList = /*#__PURE__*/ query<GatewayList>(op_dbGateways_list);
