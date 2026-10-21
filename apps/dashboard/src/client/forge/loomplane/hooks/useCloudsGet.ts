import { query } from '@forge-go/client-core';
import { op_clouds_get } from '../ops/clouds.get';
import type { Profile } from '../types';

export const useCloudsGet = /*#__PURE__*/ query<Profile>(op_clouds_get);
