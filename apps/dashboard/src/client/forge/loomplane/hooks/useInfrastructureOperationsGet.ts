import { query } from '@forge-go/client-core';
import { op_infrastructureOperations_get } from '../ops/infrastructureOperations.get';
import type { Operation } from '../types';

export const useInfrastructureOperationsGet = /*#__PURE__*/ query<Operation>(op_infrastructureOperations_get);
