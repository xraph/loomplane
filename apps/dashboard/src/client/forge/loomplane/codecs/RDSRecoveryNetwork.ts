import type { Codec } from '../codec-runtime';

export const codec_RDSRecoveryNetwork: Codec = {"kind": "object", "fields": {"security_group_id": {"ts": "securityGroupId"}, "subnet_group": {"ts": "subnetGroup"}, "verifier_group_id": {"ts": "verifierGroupId"}, "vpc_id": {"ts": "vpcId"}}, "required": ["security_group_id", "subnet_group", "verifier_group_id", "vpc_id"]};
