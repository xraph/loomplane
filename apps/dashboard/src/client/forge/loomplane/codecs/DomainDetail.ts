import type { Codec } from '../codec-runtime';
import { codec_DomainDetail_records } from './DomainDetail.records';
import { codec_DomainDetail_subdomains } from './DomainDetail.subdomains';
import { codec_Registration } from './Registration';
import { codec_ZoneOverview } from './ZoneOverview';

export const codec_DomainDetail: Codec = {"kind": "object", "fields": {"domain": {"ts": "domain", "codec": () => codec_ZoneOverview}, "parent": {"ts": "parent", "codec": () => codec_ZoneOverview}, "proof_zone": {"ts": "proofZone", "codec": () => codec_ZoneOverview}, "records": {"ts": "records", "codec": () => codec_DomainDetail_records}, "registration": {"ts": "registration", "codec": () => codec_Registration}, "subdomains": {"ts": "subdomains", "codec": () => codec_DomainDetail_subdomains}}, "required": ["domain", "records", "subdomains"]};
