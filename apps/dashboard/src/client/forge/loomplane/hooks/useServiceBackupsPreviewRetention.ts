import { query } from '@forge-go/client-core';
import { op_serviceBackups_previewRetention } from '../ops/serviceBackups.previewRetention';
import type { RetentionPreview } from '../types';

export const useServiceBackupsPreviewRetention = /*#__PURE__*/ query<RetentionPreview>(op_serviceBackups_previewRetention);
