import type { Codec } from '../codec-runtime';
import { codec_ManagedRegistryBackup } from './ManagedRegistryBackup';

export const codec_ManagedRegistryView_backups: Codec = {"kind": "array", "items": () => codec_ManagedRegistryBackup};
