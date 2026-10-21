import { mutation } from '@forge-go/client-core';
import { op_infrastructureOperations_cancel } from '../ops/infrastructureOperations.cancel';
import type { Operation } from '../types';

export const useInfrastructureOperationsCancel = /*#__PURE__*/ mutation<Operation, Operation>(op_infrastructureOperations_cancel);
