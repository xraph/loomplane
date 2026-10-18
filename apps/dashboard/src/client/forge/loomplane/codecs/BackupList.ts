import type { Codec } from '../codec-runtime';
import { codec_BackupList_backups } from './BackupList.backups';

export const codec_BackupList: Codec = {"kind": "object", "fields": {"backups": {"ts": "backups", "codec": () => codec_BackupList_backups}}, "required": ["backups"]};
