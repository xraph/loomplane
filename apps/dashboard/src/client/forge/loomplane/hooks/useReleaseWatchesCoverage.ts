import { query } from '@forge-go/client-core';
import { op_releaseWatches_coverage } from '../ops/releaseWatches.coverage';
import type { Coverage } from '../types';

export const useReleaseWatchesCoverage = /*#__PURE__*/ query<Coverage>(op_releaseWatches_coverage);
