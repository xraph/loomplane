import { mutation } from '@forge-go/client-core';
import { op_defaultCloud_preview } from '../ops/defaultCloud.preview';
import type { Preview } from '../types';

export const useDefaultCloudPreview = /*#__PURE__*/ mutation<Preview, Preview>(op_defaultCloud_preview);
