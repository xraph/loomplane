import { query } from '@forge-go/client-core';
import { op_releaseReports_get } from '../ops/releaseReports.get';
import type { ReleaseReport } from '../types';

export const useReleaseReportsGet = /*#__PURE__*/ query<ReleaseReport>(op_releaseReports_get);
