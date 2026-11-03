import { mutation } from '@forge-go/client-core';
import { op_deployments_cancel } from '../ops/deployments.cancel';
import type { DeploymentsDeployment } from '../types';

export const useDeploymentsCancel = /*#__PURE__*/ mutation<DeploymentsDeployment, DeploymentsDeployment>(op_deployments_cancel);
