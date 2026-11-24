module github.com/xraph/loomplane

go 1.27.4

require (
	cloud.google.com/go/cloudsqlconn v1.27.0
	github.com/BurntSushi/toml v1.6.0
	github.com/aws/aws-sdk-go-v2 v1.48.0
	github.com/aws/aws-sdk-go-v2/credentials v1.21.0
	github.com/aws/aws-sdk-go-v2/service/cloudwatch v1.76.0
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.92.2
	github.com/aws/aws-sdk-go-v2/service/codebuild v1.78.12
	github.com/aws/aws-sdk-go-v2/service/costexplorer v1.74.9
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.347.0
	github.com/aws/aws-sdk-go-v2/service/ecr v1.67.6
	github.com/aws/aws-sdk-go-v2/service/ecs v1.105.1
	github.com/aws/aws-sdk-go-v2/service/elasticache v1.65.0
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.64.0
	github.com/aws/aws-sdk-go-v2/service/iam v1.66.0
	github.com/aws/aws-sdk-go-v2/service/rds v1.133.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.51.0
	github.com/aws/aws-sdk-go-v2/service/servicediscovery v1.49.12
	github.com/aws/aws-sdk-go-v2/service/sts v1.52.0
	github.com/aws/smithy-go v1.28.9
	github.com/containernetworking/cni v1.3.1
	github.com/firecracker-microvm/firecracker-go-sdk v1.0.1-0.20251224190957-6fb280e993d4
	github.com/go-sql-driver/mysql v1.10.1
	github.com/gobwas/ws v1.5.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/jsonschema-go v0.4.3
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674
	github.com/hallproof/witness v0.2.0
	github.com/hallproof/witness/authsome v0.2.0
	github.com/hallproof/witness/chronicle v0.2.0
	github.com/hallproof/witness/cortex v0.2.0
	github.com/hallproof/witness/dispatch v0.2.0
	github.com/hallproof/witness/forge v0.2.0
	github.com/hallproof/witness/grove v0.2.0
	github.com/hallproof/witness/keysmith v0.2.0
	github.com/hallproof/witness/nexus v0.2.0
	github.com/hallproof/witness/relay v0.2.0
	github.com/hallproof/witness/shield v0.2.0
	github.com/hallproof/witness/trove v0.2.0
	github.com/hallproof/witness/vault v0.2.0
	github.com/hallproof/witness/warden v0.2.0
	github.com/hashicorp/yamux v0.1.2
	github.com/jackc/pgx/v5 v5.13.0
	github.com/joho/godotenv v1.5.1
	github.com/mdlayher/vsock v1.4.0
	github.com/miekg/dns v1.1.73
	github.com/moby/patternmatcher v0.6.1
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/nats-io/nats.go v1.55.0
	github.com/rabbitmq/amqp091-go v1.16.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/sigstore/protobuf-specs v0.5.2
	github.com/sigstore/sigstore-go v1.4.0
	github.com/spiffe/go-spiffe/v2 v2.9.0
	github.com/spiffe/spire-api-sdk v1.16.0
	github.com/stripe/stripe-go/v87 v87.1.0
	github.com/vishvananda/netlink v1.3.1
	github.com/vishvananda/netns v0.0.5
	github.com/xraph/authsome v1.12.4
	github.com/xraph/bastion v1.4.4
	github.com/xraph/chronicle v1.11.2
	github.com/xraph/chronicle/extension v1.11.2
	github.com/xraph/chronicle/store/postgres v1.11.2
	github.com/xraph/confy v1.1.0
	github.com/xraph/cortex v1.10.8
	github.com/xraph/cortex/extension v1.10.8
	github.com/xraph/ctrlplane v1.8.3
	github.com/xraph/dispatch v1.9.11
	github.com/xraph/forge v1.14.1
	github.com/xraph/forge/extensions/conduit v1.14.1
	github.com/xraph/go-utils v1.5.1
	github.com/xraph/grove v1.10.6
	github.com/xraph/grove/drivers/pgdriver v1.10.5
	github.com/xraph/grove/drivers/sqlitedriver v1.10.5
	github.com/xraph/grove/extension v1.10.5
	github.com/xraph/herald v1.10.5
	github.com/xraph/herald/drivers/apns v1.10.5
	github.com/xraph/keysmith v1.9.3
	github.com/xraph/ledger v1.9.5
	github.com/xraph/nexus v1.9.5
	github.com/xraph/nexus/providers/anthropic v1.9.5
	github.com/xraph/nexus/providers/openai v1.9.5
	github.com/xraph/nexus/providers/opencompat v1.9.5
	github.com/xraph/relay v1.8.4
	github.com/xraph/shield v1.10.4
	github.com/xraph/shield/integrations/nexusguard v1.10.4
	github.com/xraph/trove v1.8.4
	github.com/xraph/trove/drivers/s3driver v1.8.4
	github.com/xraph/trove/extension v1.8.4
	github.com/xraph/vault v1.8.4
	github.com/xraph/warden v1.8.5
	github.com/xraph/weave v1.9.7
	github.com/xraph/weave/embedder/nexus v1.9.7
	go.jetify.com/typeid/v2 v2.0.0-alpha.3
	go.mongodb.org/mongo-driver/v2 v2.10.0
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/trace v1.47.0
	golang.org/x/crypto v0.59.0
	golang.org/x/mod v0.43.0
	golang.org/x/net v0.62.0
	golang.org/x/oauth2 v0.38.0
	golang.org/x/sync v0.25.0
	golang.org/x/sys v0.50.0
	golang.org/x/term v0.48.0
	golang.org/x/text v0.44.0
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
	k8s.io/api v0.37.2
	k8s.io/apimachinery v0.37.2
	k8s.io/client-go v0.37.2
	k8s.io/utils v0.0.0-20261023123148-ca04ba58ecab
	modernc.org/sqlite v1.62.0
	sigs.k8s.io/json v0.0.0-20260909141634-11ed52e25bc5
	sigs.k8s.io/yaml v1.6.0
)

require (
	cloud.google.com/go/auth v0.24.4 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.3.1 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	cloud.google.com/go/monitoring v1.32.1 // indirect
	cloud.google.com/go/sql v0.7.0 // indirect
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric v0.62.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping v0.62.0 // indirect
	github.com/Microsoft/go-winio v0.6.4 // indirect
	github.com/ProtonMail/go-crypto v1.5.2 // indirect
	github.com/asaskevich/govalidator v0.0.0-20230301143203-a9d515a09cc2 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.27 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.33.18 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.9 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.13.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.39.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.44.5 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/boombuler/barcode v1.1.0 // indirect
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/containerd/fifo v1.1.0 // indirect
	github.com/containernetworking/plugins v1.10.0 // indirect
	github.com/coreos/go-oidc/v3 v3.21.0 // indirect
	github.com/coreos/go-semver v0.3.1 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/cyberphone/json-canonicalization v0.0.0-20261011152517-32a48195b9b2 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/digitorus/pkcs7 v0.0.0-20261104125046-a476de3098e4 // indirect
	github.com/digitorus/timestamp v0.0.0-20260914073129-b4b58b92aa51 // indirect
	github.com/dunglas/httpsfv v1.1.2 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/fxamacker/cbor/v2 v2.10.0 // indirect
	github.com/gabriel-vasile/mimetype v1.4.16 // indirect
	github.com/go-errors/errors v1.5.1 // indirect
	github.com/go-jose/go-jose/v4 v4.1.9 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/analysis v1.0.1 // indirect
	github.com/go-openapi/errors v0.22.9 // indirect
	github.com/go-openapi/jsonpointer v1.0.2 // indirect
	github.com/go-openapi/jsonreference v1.0.3 // indirect
	github.com/go-openapi/loads v0.25.3 // indirect
	github.com/go-openapi/runtime v0.33.4 // indirect
	github.com/go-openapi/runtime/server-middleware v0.33.4 // indirect
	github.com/go-openapi/spec v1.0.1 // indirect
	github.com/go-openapi/strfmt v0.27.4 // indirect
	github.com/go-openapi/swag v0.29.2 // indirect
	github.com/go-openapi/swag/cmdutils v0.29.2 // indirect
	github.com/go-openapi/swag/conv v0.29.2 // indirect
	github.com/go-openapi/swag/fileutils v0.29.2 // indirect
	github.com/go-openapi/swag/jsonutils v0.29.2 // indirect
	github.com/go-openapi/swag/loading v0.29.2 // indirect
	github.com/go-openapi/swag/mangling v0.29.2 // indirect
	github.com/go-openapi/swag/netutils v0.29.2 // indirect
	github.com/go-openapi/swag/pools v0.29.2 // indirect
	github.com/go-openapi/swag/stringutils v0.29.2 // indirect
	github.com/go-openapi/swag/typeutils v0.29.2 // indirect
	github.com/go-openapi/swag/yamlutils v0.29.2 // indirect
	github.com/go-openapi/validate v1.0.1 // indirect
	github.com/go-playground/locales v0.14.2 // indirect
	github.com/go-playground/universal-translator v0.18.2 // indirect
	github.com/go-playground/validator/v10 v10.30.6 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/webauthn v0.18.2 // indirect
	github.com/go-webauthn/x v0.3.1 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gofrs/uuid/v5 v5.5.1 // indirect
	github.com/golang/groupcache v0.0.0-20241129210726-2c02b8208cf8 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/certificate-transparency-go v1.4.0 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/google/go-containerregistry v0.22.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.25 // indirect
	github.com/googleapis/gax-go/v2 v2.27.0 // indirect
	github.com/grafana/pyroscope-go v1.5.0 // indirect
	github.com/grafana/pyroscope-go/godeltaprof v0.1.12 // indirect
	github.com/grandcat/zeroconf v1.0.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.33.0 // indirect
	github.com/hashicorp/consul/api v1.35.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-metrics v0.7.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-rootcerts v1.1.0 // indirect
	github.com/hashicorp/golang-lru v1.0.2 // indirect
	github.com/hashicorp/serf v0.11.0 // indirect
	github.com/in-toto/attestation v1.2.0 // indirect
	github.com/in-toto/in-toto-golang v0.11.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/jedisct1/go-minisign v0.0.0-20211028175153-1c139d1cc84b // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mdlayher/socket v0.8.0 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/moby/spdystream v0.5.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/monochromegane/go-gitignore v0.0.0-20200626010858-205db1a8cc00 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nkeys v0.4.17 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/oklog/ulid/v2 v2.1.2 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/pandatix/go-abnf v0.5.5 // indirect
	github.com/philhofer/fwd v1.2.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/pquerna/otp v1.5.0 // indirect
	github.com/prometheus/client_golang v1.25.0 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.74.0 // indirect
	github.com/prometheus/procfs v0.23.0 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.62.0 // indirect
	github.com/quic-go/webtransport-go v0.13.0 // indirect
	github.com/redis/go-redis/v9 v9.23.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/sassoftware/relic/v8 v8.2.0 // indirect
	github.com/secure-systems-lab/go-securesystemslib v0.11.1 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/segmentio/kafka-go v0.4.51 // indirect
	github.com/shibumi/go-pathspec v1.3.0 // indirect
	github.com/shopspring/decimal v1.6.0 // indirect
	github.com/sigstore/rekor v1.6.1 // indirect
	github.com/sigstore/rekor-tiles/v2 v2.4.2 // indirect
	github.com/sigstore/sigstore v1.11.3 // indirect
	github.com/sigstore/timestamp-authority/v2 v2.2.0 // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.1.0 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/theupdateframework/go-tuf v0.7.0 // indirect
	github.com/theupdateframework/go-tuf/v2 v2.4.2 // indirect
	github.com/tinylib/msgp v1.6.5 // indirect
	github.com/transparency-dev/formats v0.1.1 // indirect
	github.com/transparency-dev/merkle v0.0.2 // indirect
	github.com/uptrace/bunrouter v1.0.23 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xdg-go/scram v1.4.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/xlab/treeprint v1.2.0 // indirect
	github.com/xraph/chronicle/store/sqlite v1.11.2 // indirect
	github.com/xraph/cortex/optimization v1.10.8 // indirect
	github.com/xraph/cortex/sentinel v1.10.8 // indirect
	github.com/xraph/farp v1.3.1 // indirect
	github.com/xraph/farp/discovery v1.3.1 // indirect
	github.com/xraph/forge/extensions/auth v1.14.1 // indirect
	github.com/xraph/forge/extensions/discovery v1.14.1 // indirect
	github.com/xraph/grove/drivers/mongodriver v1.10.5 // indirect
	github.com/xraph/grove/kv v1.10.5 // indirect
	github.com/xraph/grove/kv/drivers/redisdriver v1.10.5 // indirect
	github.com/xraph/sentinel v1.10.3 // indirect
	github.com/xraph/vessel v1.0.8 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	github.com/zeebo/blake3 v0.2.4 // indirect
	go.etcd.io/etcd/api/v3 v3.7.3 // indirect
	go.etcd.io/etcd/client/pkg/v3 v3.7.3 // indirect
	go.etcd.io/etcd/client/v3 v3.7.3 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/log v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	go.yaml.in/yaml/v2 v2.4.5 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/exp v0.0.0-20261113113407-e3970619a1a4 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/api v0.315.0 // indirect
	google.golang.org/genproto v0.0.0-20261110000943-09e21b237f2c // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261110000943-09e21b237f2c // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261110000943-09e21b237f2c // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	k8s.io/streaming v0.37.2 // indirect
	modernc.org/libc v1.81.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	sigs.k8s.io/kustomize/api v0.21.1 // indirect
	sigs.k8s.io/kustomize/kyaml v0.21.1 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
)
