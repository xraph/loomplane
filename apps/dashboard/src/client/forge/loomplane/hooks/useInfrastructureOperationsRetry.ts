import { mutation } from '@forge-go/client-core';
import { op_infrastructureOperations_retry } from '../ops/infrastructureOperations.retry';
import type { Operation } from '../types';

export const useInfrastructureOperationsRetry = /*#__PURE__*/ mutation<Operation, Operation>(op_infrastructureOperations_retry);
