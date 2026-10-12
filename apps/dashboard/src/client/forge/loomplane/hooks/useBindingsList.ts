import { query } from '@forge-go/client-core';
import { op_bindings_list } from '../ops/bindings.list';
import type { BindingList } from '../types';

export const useBindingsList = /*#__PURE__*/ query<BindingList>(op_bindings_list);
