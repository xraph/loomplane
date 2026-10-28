import { mutation } from '@forge-go/client-core';
import { op_deployments_resumeCapacity } from '../ops/deployments.resumeCapacity';
import type { Deployment } from '../types';

export const useDeploymentsResumeCapacity = /*#__PURE__*/ mutation<Deployment, Deployment>(op_deployments_resumeCapacity);
