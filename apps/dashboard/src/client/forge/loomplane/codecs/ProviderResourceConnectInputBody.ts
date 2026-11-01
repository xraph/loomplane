import type { Codec } from '../codec-runtime';
import { codec_ProviderResourceConnectInputBody_bucket_access } from './ProviderResourceConnectInputBody.bucket_access';

export const codec_ProviderResourceConnectInputBody: Codec = {"kind": "object", "fields": {"bucket_access": {"ts": "bucketAccess", "codec": () => codec_ProviderResourceConnectInputBody_bucket_access}, "dsn": {"ts": "dsn"}}};
