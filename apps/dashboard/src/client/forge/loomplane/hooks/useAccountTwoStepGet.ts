import { query } from '@forge-go/client-core';
import { op_account_twoStep_get } from '../ops/account.twoStep.get';
import type { TwoStep } from '../types';

export const useAccountTwoStepGet = /*#__PURE__*/ query<TwoStep>(op_account_twoStep_get);
