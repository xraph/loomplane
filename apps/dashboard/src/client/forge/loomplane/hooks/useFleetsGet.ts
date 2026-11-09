import { query } from '@forge-go/client-core';
import { op_fleets_get } from '../ops/fleets.get';
import type { Fleet } from '../types';

export const useFleetsGet = /*#__PURE__*/ query<Fleet>(op_fleets_get);
