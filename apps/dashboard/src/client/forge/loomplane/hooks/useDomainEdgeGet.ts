import { query } from '@forge-go/client-core';
import { op_domainEdge_get } from '../ops/domainEdge.get';
import type { EdgeState } from '../types';

export const useDomainEdgeGet = /*#__PURE__*/ query<EdgeState>(op_domainEdge_get);
