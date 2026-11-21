import { query } from '@forge-go/client-core';
import { op_defaultCloud_get } from '../ops/defaultCloud.get';
import type { DefaultCloud } from '../types';

export const useDefaultCloudGet = /*#__PURE__*/ query<DefaultCloud>(op_defaultCloud_get);
