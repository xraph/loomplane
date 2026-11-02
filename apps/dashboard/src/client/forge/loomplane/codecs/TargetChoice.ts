import type { Codec } from '../codec-runtime';
import { codec_GPURequest } from './GPURequest';

export const codec_TargetChoice: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "gpu": {"ts": "gpu", "codec": () => codec_GPURequest}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "size_id": {"ts": "sizeId"}, "source": {"ts": "source"}}, "required": ["connection_id", "source"]};
