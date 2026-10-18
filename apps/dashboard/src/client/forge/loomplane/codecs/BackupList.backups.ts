import type { Codec } from '../codec-runtime';
import { codec_Backup } from './Backup';

export const codec_BackupList_backups: Codec = {"kind": "array", "items": () => codec_Backup};
