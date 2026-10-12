import { mutation } from '@forge-go/client-core';
import { op_sources_update } from '../ops/sources.update';
import type { Source } from '../types';

export const useSourcesUpdate = /*#__PURE__*/ mutation<Source, Source>(op_sources_update);
