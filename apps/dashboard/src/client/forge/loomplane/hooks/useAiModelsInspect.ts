import { mutation } from '@forge-go/client-core';
import { op_aiModels_inspect } from '../ops/aiModels.inspect';
import type { EntryDetail } from '../types';

export const useAiModelsInspect = /*#__PURE__*/ mutation<EntryDetail, EntryDetail>(op_aiModels_inspect);
