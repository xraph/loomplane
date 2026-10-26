import type { Codec } from '../codec-runtime';
import { codec_CloudSQLSourceEndpoint } from './CloudSQLSourceEndpoint';
import { codec_ManagedDatabaseProfile } from './ManagedDatabaseProfile';

export const codec_ManagedDatabaseConfig: Codec = {"kind": "object", "fields": {"account_identity": {"ts": "accountIdentity"}, "cloud_sql_psc": {"ts": "cloudSqlPsc", "codec": () => codec_CloudSQLSourceEndpoint}, "connection_id": {"ts": "connectionId"}, "profile": {"ts": "profile", "codec": () => codec_ManagedDatabaseProfile}}, "required": ["account_identity", "connection_id", "profile"]};
