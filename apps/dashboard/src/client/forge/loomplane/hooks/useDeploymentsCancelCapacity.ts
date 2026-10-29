import { mutation } from '@forge-go/client-core';
import { op_deployments_cancelCapacity } from '../ops/deployments.cancelCapacity';
import type { DeploymentsDeployment } from '../types';

export const useDeploymentsCancelCapacity = /*#__PURE__*/ mutation<DeploymentsDeployment, DeploymentsDeployment>(op_deployments_cancelCapacity);
