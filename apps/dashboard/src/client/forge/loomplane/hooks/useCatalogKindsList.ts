import { query } from '@forge-go/client-core';
import { op_catalogKinds_list } from '../ops/catalogKinds.list';
import type { CatalogKindList } from '../types';

export const useCatalogKindsList = /*#__PURE__*/ query<CatalogKindList>(op_catalogKinds_list);
