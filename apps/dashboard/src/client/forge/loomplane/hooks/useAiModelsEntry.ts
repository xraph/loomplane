import { query } from '@forge-go/client-core';
import { op_aiModels_entry } from '../ops/aiModels.entry';
import type { EntryDetail } from '../types';

export const useAiModelsEntry = /*#__PURE__*/ query<EntryDetail>(op_aiModels_entry);
