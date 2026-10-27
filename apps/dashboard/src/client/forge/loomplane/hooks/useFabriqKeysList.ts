import { query } from '@forge-go/client-core';
import { op_fabriqKeys_list } from '../ops/fabriqKeys.list';
import type { FabriqKeyList } from '../types';

export const useFabriqKeysList = /*#__PURE__*/ query<FabriqKeyList>(op_fabriqKeys_list);
