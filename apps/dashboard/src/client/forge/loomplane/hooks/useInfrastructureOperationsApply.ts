import { mutation } from '@forge-go/client-core';
import { op_infrastructureOperations_apply } from '../ops/infrastructureOperations.apply';
import type { Operation } from '../types';

export const useInfrastructureOperationsApply = /*#__PURE__*/ mutation<Operation, Operation>(op_infrastructureOperations_apply);
