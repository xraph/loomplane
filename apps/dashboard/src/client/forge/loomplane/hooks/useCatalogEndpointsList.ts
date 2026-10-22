import { query } from '@forge-go/client-core';
import { op_catalogEndpoints_list } from '../ops/catalogEndpoints.list';
import type { EndpointList } from '../types';

export const useCatalogEndpointsList = /*#__PURE__*/ query<EndpointList>(op_catalogEndpoints_list);
