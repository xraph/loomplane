import type { Codec } from '../codec-runtime';
import { codec_MigrationFile } from './MigrationFile';

export const codec_SourceDiff_migrations: Codec = {"kind": "array", "items": () => codec_MigrationFile};
