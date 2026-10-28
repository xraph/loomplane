import { mutation } from '@forge-go/client-core';
import { op_deployments_cancelCapacity } from '../ops/deployments.cancelCapacity';
import type { Deployment } from '../types';

export const useDeploymentsCancelCapacity = /*#__PURE__*/ mutation<Deployment, Deployment>(op_deployments_cancelCapacity);
