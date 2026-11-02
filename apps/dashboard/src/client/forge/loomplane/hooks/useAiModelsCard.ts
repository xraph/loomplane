import { query } from '@forge-go/client-core';
import { op_aiModels_card } from '../ops/aiModels.card';
import type { Card } from '../types';

export const useAiModelsCard = /*#__PURE__*/ query<Card>(op_aiModels_card);
