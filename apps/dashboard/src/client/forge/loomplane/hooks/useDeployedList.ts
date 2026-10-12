import { query } from '@forge-go/client-core';
import { op_deployed_list } from '../ops/deployed.list';
import type { DeployedList } from '../types';

export const useDeployedList = /*#__PURE__*/ query<DeployedList>(op_deployed_list);
