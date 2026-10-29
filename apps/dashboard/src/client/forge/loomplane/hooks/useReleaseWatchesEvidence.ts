import { query } from '@forge-go/client-core';
import { op_releaseWatches_evidence } from '../ops/releaseWatches.evidence';
import type { ReleaseEvidence } from '../types';

export const useReleaseWatchesEvidence = /*#__PURE__*/ query<ReleaseEvidence>(op_releaseWatches_evidence);
