import { mutation } from '@forge-go/client-core';
import { op_sources_create } from '../ops/sources.create';
import type { Source } from '../types';

export const useSourcesCreate = /*#__PURE__*/ mutation<Source, Source>(op_sources_create);
