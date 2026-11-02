import type { Codec } from '../codec-runtime';
import { codec_DomainCheckInputBody_domains } from './DomainCheckInputBody.domains';

export const codec_DomainCheckInputBody: Codec = {"kind": "object", "fields": {"connection_id": {"ts": "connectionId"}, "domains": {"ts": "domains", "codec": () => codec_DomainCheckInputBody_domains}}, "required": ["connection_id", "domains"]};
