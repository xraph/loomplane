import { mutation } from '@forge-go/client-core';
import { op_releaseReports_cancel } from '../ops/releaseReports.cancel';
import type { ReleaseReport } from '../types';

export const useReleaseReportsCancel = /*#__PURE__*/ mutation<ReleaseReport, ReleaseReport>(op_releaseReports_cancel);
