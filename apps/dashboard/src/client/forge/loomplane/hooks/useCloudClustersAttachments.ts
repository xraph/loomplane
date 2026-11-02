import { query } from '@forge-go/client-core';
import { op_cloudClusters_attachments } from '../ops/cloudClusters.attachments';
import type { RuntimeAttachmentPage } from '../types';

export const useCloudClustersAttachments = /*#__PURE__*/ query<RuntimeAttachmentPage>(op_cloudClusters_attachments);
