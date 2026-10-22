import { mutation } from '@forge-go/client-core';
import { op_cloudServers_preview } from '../ops/cloudServers.preview';
import type { Operation } from '../types';

export const useCloudServersPreview = /*#__PURE__*/ mutation<Operation, Operation>(op_cloudServers_preview);
