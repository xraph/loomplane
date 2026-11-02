import { query } from '@forge-go/client-core';
import { op_fabriqRebuilds_list } from '../ops/fabriqRebuilds.list';
import type { RebuildList } from '../types';

export const useFabriqRebuildsList = /*#__PURE__*/ query<RebuildList>(op_fabriqRebuilds_list);
