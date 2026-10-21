import { query } from '@forge-go/client-core';
import { op_infrastructureOperations_list } from '../ops/infrastructureOperations.list';
import type { OperationList } from '../types';

export const useInfrastructureOperationsList = /*#__PURE__*/ query<OperationList>(op_infrastructureOperations_list);
