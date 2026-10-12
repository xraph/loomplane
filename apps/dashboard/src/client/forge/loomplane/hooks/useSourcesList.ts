import { query } from '@forge-go/client-core';
import { op_sources_list } from '../ops/sources.list';
import type { SourceList } from '../types';

export const useSourcesList = /*#__PURE__*/ query<SourceList>(op_sources_list);
