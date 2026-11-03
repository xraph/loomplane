import { query } from '@forge-go/client-core';
import { op_nodeJoinTokens_list } from '../ops/nodeJoinTokens.list';
import type { JoinTokenList } from '../types';

export const useNodeJoinTokensList = /*#__PURE__*/ query<JoinTokenList>(op_nodeJoinTokens_list);
