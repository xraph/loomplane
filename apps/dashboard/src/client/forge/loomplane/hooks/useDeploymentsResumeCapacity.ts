import { mutation } from '@forge-go/client-core';
import { op_deployments_resumeCapacity } from '../ops/deployments.resumeCapacity';
import type { DeploymentsDeployment } from '../types';

export const useDeploymentsResumeCapacity = /*#__PURE__*/ mutation<DeploymentsDeployment, DeploymentsDeployment>(op_deployments_resumeCapacity);
