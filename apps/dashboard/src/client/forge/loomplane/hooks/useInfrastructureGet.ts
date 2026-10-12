import { query } from '@forge-go/client-core';
import { op_infrastructure_get } from '../ops/infrastructure.get';
import type { InfrastructureStatus } from '../types';

export const useInfrastructureGet = /*#__PURE__*/ query<InfrastructureStatus>(op_infrastructure_get);
