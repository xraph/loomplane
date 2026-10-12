import { query } from '@forge-go/client-core';
import { op_checkOrgSlug } from '../ops/checkOrgSlug';
import type { SlugAvailableResponse } from '../types';

export const useCheckOrgSlug = /*#__PURE__*/ query<SlugAvailableResponse>(op_checkOrgSlug);
