import { query } from '@forge-go/client-core';
import { op_deployments_list } from '../ops/deployments.list';
import type { DeploymentList } from '../types';

export const useDeploymentsList = /*#__PURE__*/ query<DeploymentList>(op_deployments_list);
