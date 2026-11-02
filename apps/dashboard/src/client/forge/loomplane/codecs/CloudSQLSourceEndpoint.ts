import type { Codec } from '../codec-runtime';

export const codec_CloudSQLSourceEndpoint: Codec = {"kind": "object", "fields": {"consumer_project": {"ts": "consumerProject"}, "forwarding_rule": {"ts": "forwardingRule"}, "network": {"ts": "network"}, "provision_subnetwork": {"ts": "provisionSubnetwork"}}, "required": ["consumer_project", "forwarding_rule", "network"]};
