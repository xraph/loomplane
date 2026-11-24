import { query } from '@forge-go/client-core';
import { op_catalogEndpoints_uses } from '../ops/catalogEndpoints.uses';
import type { ServiceUseList } from '../types';

export const useCatalogEndpointsUses = /*#__PURE__*/ query<ServiceUseList>(op_catalogEndpoints_uses);
