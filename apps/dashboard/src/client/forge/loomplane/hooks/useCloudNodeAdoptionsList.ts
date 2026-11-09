import { query } from '@forge-go/client-core';
import { op_cloudNodeAdoptions_list } from '../ops/cloudNodeAdoptions.list';
import type { NodeAdoptionList } from '../types';

export const useCloudNodeAdoptionsList = /*#__PURE__*/ query<NodeAdoptionList>(op_cloudNodeAdoptions_list);
