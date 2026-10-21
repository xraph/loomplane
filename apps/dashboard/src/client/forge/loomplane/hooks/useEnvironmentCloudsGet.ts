import { query } from '@forge-go/client-core';
import { op_environmentClouds_get } from '../ops/environmentClouds.get';
import type { EnvironmentCloud } from '../types';

export const useEnvironmentCloudsGet = /*#__PURE__*/ query<EnvironmentCloud>(op_environmentClouds_get);
