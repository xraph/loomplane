// Generated types

export enum ConnectionState {
  DISCONNECTED = 'disconnected',
  CONNECTING = 'connecting',
  CONNECTED = 'connected',
  RECONNECTING = 'reconnecting',
  CLOSED = 'closed',
  ERROR = 'error',
}

export interface APIKey {
  access: string;
  createdAt: string;
  createdBy: string;
  expiresAt?: string;
  hint: string;
  id: string;
  lastUsedAt?: string;
  name: string;
  revokedAt?: string;
  state: string;
}

export interface APIKeyList {
  keys: APIKey[];
}

export interface Accelerator {
  count: number;
  kind: string;
  memoryGib: number;
  vendor: string;
}

export interface AccountSpend {
  cloud: string;
  connectionId: string;
  currency: string;
  days: Point[];
  name: string;
  total: number;
}

export interface Action {
  adoptCloudPlacement?: boolean;
  backupEveryHours?: number;
  changes?: Change[];
  cloudAdoption?: CloudAdoption;
  compensation?: CloudCompensation;
  connectionId?: string;
  creates: string;
  dependsOn?: string[];
  engine: string;
  id: string;
  name: string;
  op: string;
  provider: string;
  reason: string;
  region?: string;
  resource?: ResourceConfig;
  routes?: CloudinfraRoute[];
  service?: ServiceConfig;
  step: number;
  subject: string;
}

export interface ActivityDay {
  date: string;
  failed: number;
  succeeded: number;
}

export interface ActivitySummary {
  analyses: AnalysisCounts;
  daily: ActivityDay[];
  days: number;
  deployments: DeploymentCounts;
  drift: DriftCounts;
  plans: PlanCounts;
  since: string;
}

export interface AddSourceInputBody {
  alias?: string;
  ref?: string;
  repository: string;
  root?: string;
}

export interface Allocation {
  environmentId?: string;
  label: string;
  monthlyBudgetUsd: string;
  projectId?: string;
  scope: string;
  updatedAt: string;
}

export interface Amount {
  currency: string;
  days: Point[];
  total: number;
  undated: number;
}

export interface Analysis {
  commitSha: string;
  createdAt: string;
  createdBy: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  pinned: boolean;
  projectId: string;
  ref: string;
  repository: string;
  source: string;
  sourceId: string;
  startedAt?: string;
  status: string;
  summary: GraphSummary;
}

export interface AnalysisCounts {
  failed: number;
  succeeded: number;
}

export interface AnalysisGraph {
  graph: Graph;
  id: string;
}

export interface AnalysisList {
  analyses: Analysis[];
}

export interface AppInputBody {
  clientId: string;
  clientSecret: string;
  slug?: string;
}

export interface AppliedInputs {
  artifactKey?: string;
  source?: PlanSource;
  variables?: string;
}

export interface ApplyCloudInputBody {
  idempotencyKey: string;
  previewId: string;
  revision: string;
}

export interface ApplyInputBody {
  review: string;
}

export interface ApproveExpansionInputBody {
  expectedRevision: number;
}

export interface ArtifactChoice {
  from?: string;
  reason?: string;
  reference?: string;
  reused: boolean;
  revision?: string;
  service: string;
}

export interface Assignment {
  cloudId: string;
  cloudRevision: number;
  defaultPlacement?: DefaultPlacement;
  environmentId: string;
  grantRevision: number;
  orgId: string;
  projectId: string;
  revision: number;
  targets: Target[];
}

export interface AttachDefaults {
  blockedPathPrefixes: string[];
  cloudId?: string;
  environmentId: string;
  port: number;
  service: string;
}

export interface AttachmentInput {
  adoptNamespace: boolean;
  cloudRevision: number;
  endpointFingerprint: string;
  grantRevision: number;
  namespace: string;
  namespaceUid?: string;
  prerequisites: AttachmentPrerequisites;
  projectId: string;
  resourceRevision: number;
  serviceAccountName: string;
  targetSlug: string;
  trustFingerprint: string;
}

export interface AttachmentPrerequisites {
  ingressAddress?: string;
  registryHostname?: string;
  requireBuilds: boolean;
  requireIngress: boolean;
  requirePersistentStorage: boolean;
  requireTls: boolean;
  storageClass?: string;
}

export interface AutoRenewInputBody {
  autoRenew: boolean;
  revision: number;
}

export interface Backup {
  attempt: number;
  bytes: number;
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  environmentId: string;
  error?: string;
  id: string;
  name: string;
  orgId: string;
  projectId: string;
  restoredAt?: string;
  sha256?: string;
  status: string;
}

export interface BackupCreateInputBody {
  environment: string;
  requestId: string;
  resource: string;
}

export interface BackupList {
  backups: Backup[];
}

export interface Binding {
  commitSha: string;
  config: string;
  connectionId: string;
  createdAt: string;
  drift: string;
  driftMessage: string;
  engine: string;
  environmentId: string;
  externalId: string;
  id: string;
  name: string;
  observedAt?: string;
  orgId: string;
  ownedResources?: Record<string, any>;
  physicalId?: string;
  planId: string;
  projectId: string;
  provider: string;
  region: string;
  revision: string;
  subject: string;
  updatedAt: string;
  url: string;
}

export interface BindingList {
  bindings: Binding[];
}

export interface Board {
  counts: SourceCounts;
  discovery: DiscoverySummary;
  gateway: OctopusStatus;
  hosts: HostCard[];
  live: boolean;
  liveError?: string;
  unroutable: UnroutableService[];
  unrouted: ServicePort[];
}

export interface BoardRoute {
  key: string;
  methods: string[];
  note: string;
  overrides: boolean;
  path: string;
  port: number;
  revision?: number;
  routeId?: string;
  service: string;
  source: string;
  stripPrefix: boolean;
  sync?: string;
  syncError?: string;
}

export interface Build {
  builder?: string;
  context?: string;
  image?: string;
  registry?: string;
}

export interface BuildSystem {
  evidence: Evidence;
  name: string;
  path: string;
  source?: string;
}

export interface BuildTarget {
  builder?: string;
  builderSettings?: Record<string, any>;
  image?: string;
  registry?: string;
  registrySettings?: Record<string, any>;
}

export interface Canary {
  holdSeconds: number;
  reason?: string;
  services: CanaryService[];
  status: string;
  step: number;
  steps: number[];
  thresholds: Thresholds;
  updatedAt: string;
  weight: number;
}

export interface CanaryReport {
  canary?: Canary;
  verdicts: CanaryVerdict[];
}

export interface CanaryService {
  actionId: string;
  baseline?: string;
  bindingId: string;
  candidate?: string;
  image?: string;
  name: string;
  provider: string;
}

export interface CanaryVerdict {
  actionId: string;
  attempt: number;
  baseline: Health;
  baselineFallback: boolean;
  candidate: Health;
  decidedAt: string;
  deploymentId: string;
  id: string;
  orgId: string;
  projectId: string;
  reason: string;
  retry: number;
  service: string;
  step: number;
  thresholds: Thresholds;
  verdict: string;
  weight: number;
  windowEnd: string;
  windowStart: string;
}

export interface CancelResult {
  message?: string;
}

export interface CapacityAccountSnapshot {
  accountId: string;
  checkedAt: string;
  provider: string;
  provisioned: CapacityResources;
  quota: CapacityQuota;
}

export interface CapacityBlock {
  cloudId: string;
  counted: number;
  limit: number;
  policyRevision: number;
  poolId: string;
  reason: string;
  requested: number;
  resource: string;
  retryable: boolean;
  targetId: string;
}

export interface CapacityChange {
  cloudId: string;
  entityId: string;
  kind: string;
  orgId: string;
  revision: number;
  status?: string;
}

export interface CapacityDemand {
  architecture: string;
  cloudId: string;
  environmentId: string;
  intentHash: string;
  localData: boolean;
  orgId: string;
  planId: string;
  policyRevision: number;
  poolId: string;
  projectId: string;
  resources: ResourceVector;
  service: string;
  stickyServerId: string;
  targetId: string;
}

export interface CapacityIntent {
  assignmentRevision: number;
  cloudRevision: number;
  commitSha: string;
  demand: CapacityDemand;
  grantRevision: number;
  providerPin: CapacityTemplatePin;
  targetName: string;
  targetRevision: number;
  template: PoolExpansionPolicy;
  templateHash: string;
}

export interface CapacityObservation {
  checkedAt: string;
  cpuMillis?: number;
  freeStorageBytes?: number;
  memoryBytes?: number;
  storageBytes?: number;
}

export interface CapacityPolicy {
  cloudId: string;
  enabled: boolean;
  frontendLimits: FrontendLimit[];
  gpuLimits?: GPULimit[];
  limits: ResourceVector;
  orgId: string;
  pools: PoolExpansionPolicy[];
  requireExpansionApproval: boolean;
  revision: number;
}

export interface CapacityPolicyInputBody {
  expectedRevision: number;
  policy: {
  cloudId: string;
  enabled: boolean;
  frontendLimits: FrontendLimit[];
  gpuLimits?: GPULimit[];
  limits: ResourceVector;
  orgId: string;
  pools: PoolExpansionPolicy[];
  requireExpansionApproval: boolean;
  revision: number;
};
}

export interface CapacityPolicyView {
  automaticExpansion: boolean;
  cloudId: string;
  policy?: CapacityPolicy;
}

export interface CapacityQuota {
  cpuMillis?: number;
  memoryBytes?: number;
  servers?: number;
  storageBytes?: number;
}

export interface CapacityRecord {
  accountId: string;
  checkedAt: string;
  gpuKind?: string;
  gpus?: number;
  kind: string;
  observation?: CapacityObservation;
  provider: string;
  resourceId: string;
  resources: CapacityResources;
  serverId?: string;
  serverStateRevision: number;
}

export interface CapacityResources {
  cpuMillis?: number;
  memoryBytes?: number;
  servers?: number;
  storageBytes?: number;
}

export interface CapacityTemplatePin {
  accountId: string;
  connectionGeneration: number;
}

export interface CapacityUsage {
  accounts: CapacityAccountSnapshot[];
  allocatable: CapacityResources;
  checkedAt: string;
  cloudId: string;
  fresh: boolean;
  frontends: FrontendUsage[];
  gpus: GPUUsage[];
  headroom: CapacityResources;
  observed: CapacityResources;
  observedStorageBytes: number | null;
  pending: CapacityResources;
  provisioned: CapacityResources;
  records: CapacityRecord[];
  reserved: CapacityResources;
  systemReserve: CapacityResources;
}

export interface CapacityWait {
  blocks: CapacityBlock[];
  createdAt: string;
  revision: number;
}

export interface CapacityWaitInputBody {
  expectedRevision: number;
}

export interface Card {
  markdown: string;
  note?: string;
  sourceUrl: string;
}

export interface CatalogKindList {
  kinds: KindInfo[];
}

export interface CatalogconnectCreateInputBody {
  dsn: string;
  engine: string;
  name: string;
}

export interface CatalogconnectEndpoint {
  createdAt: string;
  createdBy: string;
  database?: string;
  engine: string;
  host: string;
  id: string;
  name: string;
  orgId: string;
  port: number;
  revision: number;
  tls: string;
  username?: string;
}

export interface CatalogproxySession {
  expiresAt: string;
  url: string;
}

export interface ChallengeCredentialInputBody {
  connectionId: string;
  revision: number;
}

export interface Change {
  field: string;
  from: string;
  to: string;
}

export interface ChangeInput {
  cloudId: string;
  connectionId?: string;
  default?: boolean;
  environmentId?: string;
  poolId?: string;
  provider?: string;
  region?: string;
  revision: number;
  runtime?: string;
  targetId?: string;
}

export interface ChangeInputBody {
  cloudId: string;
  connectionId?: string;
  default?: boolean;
  environmentId?: string;
  poolId?: string;
  provider?: string;
  region?: string;
  revision: number;
  runtime?: string;
  targetId?: string;
}

export interface CheckInputBody {
  dependency: string;
  dsn: string;
  engine: string;
  kind: string;
  target: string;
}

export interface CheckResult {
  missing?: string[];
  notes?: string[];
  ok: boolean;
  problems?: string[];
  version?: string;
}

export interface ChooseConnectionInputBody {
  connectionId: string;
}

export interface CleanupReview {
  connectionId: string;
  deploymentId: string;
  effect: string;
  provider: string;
  region: string;
  resource: string;
  revision: string;
}

export interface CloudAdoption {
  bindingId: string;
  configDigest: string;
  externalId: string;
  planId: string;
  revision: string;
}

export interface CloudCluster {
  accountId: string;
  cloudId: string;
  connectionId: string;
  externalId: string;
  id: string;
  lifecycleAuthority: string[];
  observation?: ClusterObservation;
  ownership: string;
  provider: string;
  revision: number;
  status: string;
  updatedAt: string;
}

export interface CloudClusterOperation {
  attempt: number;
  cancelRequested: boolean;
  cloudId: string;
  createdAt: string;
  digest: string;
  enrollment?: ClusterEnrollmentSummary;
  error: string;
  estimatedNodeHourlyMicroUsd: number;
  expiresAt: string;
  id: string;
  input: ClusterInput;
  kind: string;
  resourceId: string;
  revision: number;
  status: string;
  steps: ClusterStep[];
  unknownCosts: string[];
  updatedAt: string;
}

export interface CloudClusterOperationPage {
  items: CloudClusterOperation[];
  nextCursor?: string;
}

export interface CloudClusterPage {
  items: CloudCluster[];
  nextCursor?: string;
}

export interface CloudCompensation {
  actionId: string;
  candidate: Binding;
  originalCompleted: boolean;
  planId: string;
  retained: boolean;
  routes?: CloudinfraRoute[];
  serverAttempt: boolean;
  source: Binding;
}

export interface CloudForm {
  builtin: boolean;
  cloud: string;
  credential?: string;
  help?: string;
  inClusterSettings?: Setting[];
  keylessSettings?: Setting[];
  methods: string[];
  settings?: Setting[];
  signInSettings?: Setting[];
  title: string;
}

export interface CloudPlacement {
  accountIdentity: string;
  allocation?: string;
  assignmentRevision: number;
  cloudId: string;
  cloudRevision: number;
  connectionGeneration?: number;
  connectionId: string;
  cpuMillis?: number;
  default?: boolean;
  gpuKind?: string;
  gpus?: number;
  grantRevision?: number;
  memoryBytes?: number;
  poolId?: string;
  poolRevision?: number;
  provider: string;
  region?: string;
  regionSource?: string;
  serverId?: string;
  serverRevision?: number;
  targetId: string;
  targetName: string;
  targetRevision: number;
}

export interface CloudProjectGrantInputBody {
  targetIds: string[];
}

export interface CloudProjectGrantList {
  grants: (ProjectGrant | null)[];
}

export interface CloudSQLApplicationNetwork {
  consumerProject: string;
  network: string;
  subnetwork: string;
}

export interface CloudSQLRecoveryNetwork {
  consumerProject: string;
  network: string;
}

export interface CloudSQLSourceEndpoint {
  consumerProject: string;
  forwardingRule: string;
  network: string;
  provisionSubnetwork?: string;
}

export interface CloudSyncEvent {
  attempt: number;
  cloudId: string;
  environments: EnvironmentChange[];
  id: string;
  orgId: string;
  projectId: string;
  reverts?: string;
  status: string;
  targetId: string;
  updatedAt: string;
}

export interface CloudSyncList {
  next?: string;
  syncs: (Sync | null)[];
}

export interface CloudTargetList {
  targets: (Target | null)[];
}

export interface CloudinfraMoveInputBody {
  destinationServerId: string;
  environmentId: string;
  poolId: string;
  projectId: string;
  review?: string;
  service: string;
}

export interface CloudinfraRoute {
  binding: RouteBinding;
  id: string;
  input: RouteInput;
  operationId: string;
  orgId: string;
  projectId: string;
  receipt?: DNSRecord;
  receipts?: DNSRecord[];
  revision: number;
  status: string;
  updatedAt: string;
  verification: RouteVerification;
}

export interface CloudinfraRouteStatus {
  backend: string;
  certificate: string;
  certificateExpiresAt: string;
  checkedAt: string;
  dns: string;
  hostname: string;
  message?: string;
  operationId?: string;
  retryable: boolean;
  route: string;
  routeId: string;
  wildcard: boolean;
}

export interface CloudprofilesCreateInputBody {
  defaultPlacement?: {
  connectionId: string;
  provider: string;
  region: string;
};
  name: string;
}

export interface CloudprofilesUpdateInputBody {
  name: string;
  revision: number;
}

export interface ClusterChange {
  cloudId: string;
  operationId: string;
  reason: string;
  resourceId: string;
  revision: number;
  status: string;
}

export interface ClusterCondition {
  reason?: string;
  status: string;
  type: string;
}

export interface ClusterEnrollmentSummary {
  attachmentId: string;
  credentialExpiry: string;
  runtimeConnectionId: string;
  runtimeVerified: boolean;
  targetId: string;
}

export interface ClusterInput {
  accountId: string;
  acknowledgeNodeCosts: boolean;
  acknowledgeUnknownCosts: boolean;
  attachment?: AttachmentInput;
  cloudRevision: number;
  connectionGeneration: number;
  connectionId: string;
  kind: string;
  managementReachable: boolean;
  optionsCheckedAt: string;
  optionsDigest: string;
  policyRevision: number;
  provider: string;
  ref: ClusterRef;
  resourceId?: string;
  resourceRevision: number;
  spec: ClusterSpec;
  workerImage: string;
}

export interface ClusterInputBody {
  accountId: string;
  acknowledgeNodeCosts: boolean;
  acknowledgeUnknownCosts: boolean;
  attachment?: {
  adoptNamespace: boolean;
  cloudRevision: number;
  endpointFingerprint: string;
  grantRevision: number;
  namespace: string;
  namespaceUid?: string;
  prerequisites: AttachmentPrerequisites;
  projectId: string;
  resourceRevision: number;
  serviceAccountName: string;
  targetSlug: string;
  trustFingerprint: string;
};
  cloudRevision: number;
  connectionGeneration: number;
  connectionId: string;
  kind: string;
  managementReachable: boolean;
  optionsCheckedAt: string;
  optionsDigest: string;
  policyRevision: number;
  provider: string;
  ref: {
  accountId: string;
  id: string;
  incarnation: string;
  region: string;
};
  resourceId?: string;
  resourceRevision: number;
  spec: {
  ha: boolean;
  maintenance: ClusterMaintenance;
  name: string;
  networkId: string;
  nodePools: NodePoolSpec[];
  operationId: string;
  providerOptions?: ClusterProviderOptions;
  region: string;
  version: string;
};
  workerImage: string;
}

export interface ClusterMaintenance {
  day: string;
  startTime: string;
}

export interface ClusterNodeSize {
  cpus: number;
  diskGb: number;
  hourlyUsd?: number;
  id: string;
  memoryMib: number;
  monthlyUsd?: number;
  regions: string[];
}

export interface ClusterObservation {
  caHash: string;
  conditions: ClusterCondition[];
  endpoint: string;
  name: string;
  observedAt: string;
  pools: NodePoolSpec[];
  ref: ClusterRef;
  state: string;
  version: string;
}

export interface ClusterOptions {
  accountId: string;
  checkedAt: string;
  connectionGeneration: number;
  digest: string;
  networks: string[];
  nodeSizeFacts: ClusterNodeSize[];
  nodeSizes: string[];
  providerOptions?: ClusterProviderOptions;
  regions: string[];
  versions: string[];
}

export interface ClusterPage {
  items: ClusterObservation[];
  nextCursor?: string;
}

export interface ClusterProviderOptions {
  payload: string;
  provider: string;
  version: number;
}

export interface ClusterRef {
  accountId: string;
  id: string;
  incarnation: string;
  region: string;
}

export interface ClusterSpec {
  ha: boolean;
  maintenance: ClusterMaintenance;
  name: string;
  networkId: string;
  nodePools: NodePoolSpec[];
  operationId: string;
  providerOptions?: ClusterProviderOptions;
  region: string;
  version: string;
}

export interface ClusterStep {
  attemptedAt?: string;
  completedAt?: string;
  name: string;
  remoteOperationId?: string;
  remoteResourceId?: string;
  requestDigest: string;
  state: string;
}

export interface Collection {
  actionId: string;
  attempt: string;
  deploymentId: string;
  id: string;
  message: string;
  operationId: string;
  orgId: string;
  projectId: string;
  state: string;
  updatedAt: string;
}

export interface Commands {
  build?: string;
  dev?: string;
  start?: string;
}

export interface CompleteCloudRoutingInputBody {
  environmentId: string;
  evidence: string;
}

export interface CompleteInputBody {
  code: string;
  state: string;
}

export interface ComponentConfig {
  component: ResolvedComponent;
  dependencies?: Record<string, any>;
  gpu?: GPUPlacement;
  inputs?: string;
  kind: string;
  protection: string;
  resource: string;
  revision: string;
  settings?: Record<string, any>;
}

export interface ComponentInfo {
  name: string;
  protection: string;
  role: string;
  stateful: boolean;
}

export interface CompositionConfig {
  dependencies?: Record<string, any>;
  kind: string;
  outputs?: Output[];
  revision: string;
  secrets?: GeneratedSecret[];
  version: string;
}

export interface ConnectGitHubInputBody {
  code: string;
  installationId: number;
  state: string;
}

export interface ConnectedRepository {
  defaultBranch: string;
  fullName: string;
  installation: string;
  private: boolean;
  url: string;
}

export interface ConnectedRepositoryList {
  repositories: ConnectedRepository[];
}

export interface Connection {
  account: string;
  accountIdentity?: string;
  checkedAt?: string;
  cloud: string;
  createdAt: string;
  createdBy: string;
  details: Record<string, any>;
  generation: number;
  id: string;
  message: string;
  method: string;
  missing: string[];
  name: string;
  orgId: string;
  signedInBy?: string;
  status: string;
  updatedAt: string;
}

export interface ConnectionList {
  connections: Connection[];
}

export interface ConnectionsCloudList {
  clouds: CloudForm[];
}

export interface ConnectionsCreateInputBody {
  cloud: string;
  credential: string;
  name: string;
  settings?: Record<string, any>;
}

export interface ConsumerKey {
  createdAt?: string;
  lastUsedAt?: string;
  lastUsedKnown: boolean;
  prefix?: string;
  revokedAt?: string;
  service: string;
  state: string;
  tenant: string;
}

export interface ConsumerOption {
  name: string;
  pattern?: string;
  required?: boolean;
  title: string;
}

export interface Cost {
  estimate: boolean;
  hourlyUsd: string;
  known: boolean;
  monthlyUsd: string;
  share: string;
  source: string;
}

export interface Coverage {
  canary: string[];
  duration: number;
  environment: string;
  services: ServiceCoverage[];
  unverified: boolean;
}

export interface CreateEnvironmentInputBody {
  kind?: string;
  name: string;
}

export interface CreateInvitationInputBody {
  email: string;
  role?: string;
}

export interface CreatePlanInputBody {
  catalog?: boolean;
  environment: string;
  from?: string;
  scope?: string[];
}

export interface CreateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name: string;
  repositories?: {
  alias?: string;
  ref?: string;
  repository: string;
  root?: string;
}[];
  repositoryUrl?: string;
  slug?: string;
}

export interface CreateTargetInputBody {
  connectionId: string;
  displayName: string;
  name: string;
  poolId?: string;
  provider: string;
  region: string;
  runtime: string;
  tags?: string[];
}

export interface CreateVariableInputBody {
  environment?: string;
  key: string;
  secret?: boolean;
  service?: string;
  value: string;
}

export interface CreatedInvitation {
  acceptPath: string;
  emailMessage: string;
  emailStatus: string;
  invitation: OrgInvitation;
}

export interface CreatedKey {
  key: APIKey;
  secret: string;
}

export interface DNSRecord {
  id: string;
  name: string;
  owner?: string;
  proxied?: boolean;
  ttl: number;
  type: string;
  value: string;
  zoneId: string;
}

export interface DNSZone {
  accountId: string;
  id: string;
  name: string;
  nameservers?: string[];
  verified: boolean;
}

export interface DatabaseGateway {
  cloudTags?: string[];
  environments?: Record<string, any>;
  provider?: string;
  region?: string;
}

export interface DatabaseGatewayConfig {
  name: string;
  program: string;
  resources: string[];
  suffix: string;
  trust?: string;
}

export interface DbgatewayRouteStatus {
  backendConns: number;
  id: string;
  maxConnections: number;
  pinRate: number;
  reachable: boolean;
  reported: boolean;
  resource: string;
  state: string;
}

export interface DecideInputBody {
  note?: string;
  revision?: string;
}

export interface DefaultPlacement {
  connectionId: string;
  provider: string;
  region: string;
}

export interface Defaults {
  cloudTags?: string[];
  provider?: string;
  region?: string;
}

export interface Delivery {
  attempts: number;
  createdAt: string;
  deliveredAt?: string;
  eventType: string;
  id: string;
  nextRetryAt?: string;
  status: string;
  statusCode: number;
}

export interface DeliveryList {
  items: Delivery[];
}

export interface DependencyInfo {
  default: boolean;
  external: boolean;
  kinds: string[];
  name: string;
  optional: boolean;
  title: string;
}

export interface DeployInputBody {
  entryId: string;
  maxModelLen?: number;
  monthlyBudgetUsd?: string;
  quantization?: string;
  resource?: string;
  reviewServer?: boolean;
  server?: {
  cloudId: string;
  operationId: string;
  review: string;
};
  target: {
  connectionId: string;
  gpu?: GPURequest;
  provider?: string;
  region?: string;
  sizeId?: string;
  source: string;
};
}

export interface DeployPreview {
  addsTo?: string;
  canDeploy: boolean;
  cost: Cost;
  entry: EntrySummary;
  environments: Reach[];
  fit: Fit;
  gated: boolean;
  gpu?: GPURequest;
  kind: string;
  maxModelLen: number;
  problems: Problem[];
  quantization: string;
  resource: string;
  server?: Operation;
  serverReviewNeeded?: boolean;
  target: TargetChoice;
  targetName: string;
  tokenReady: boolean;
  version: string;
}

export interface DeployResult {
  deployment?: ModelsDeployment;
  message?: string;
  planId?: string;
}

export interface DeployedList {
  sources: DeployedRepository[];
}

export interface DeployedRepository {
  alias: string;
  commitSha: string;
  deployedAt: string;
  planId: string;
  repository: string;
  sourceId: string;
}

export interface Deployment {
  canary?: Canary;
  capacityWait?: CapacityWait;
  commitSha: string;
  environment: string;
  environmentId: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  planId: string;
  projectId: string;
  retries: number;
  sources: SourceArtifact[];
  startedAt: string;
  startedBy: string;
  status: string;
  steps: Step[];
}

export interface DeploymentCounts {
  failed: number;
  medianSeconds: number;
  retried: number;
  running: number;
  succeeded: number;
}

export interface DeploymentList {
  deployments: DeploymentsDeployment[];
  next?: string;
}

export interface DeploymentsDeployment {
  canary?: Canary;
  capacityWait?: CapacityWait;
  commitSha: string;
  environment: string;
  environmentId: string;
  error: string;
  finishedAt?: string;
  id: string;
  orgId: string;
  planId: string;
  projectId: string;
  retries: number;
  sources: SourceArtifact[];
  startedAt: string;
  startedBy: string;
  status: string;
  steps: Step[];
}

export interface DiscoveredService {
  error?: string;
  metadata?: Record<string, any>;
  optedIn: boolean;
  routes: number;
  service: string;
  state: string;
  updatedAt: string;
}

export interface DiscoverySource {
  alias: string;
  createdAt: string;
  createdBy: string;
  id: string;
  orgId: string;
  projectId: string;
  ref: string;
  removedAt?: string;
  repository: string;
  root: string;
  updatedAt: string;
}

export interface DiscoverySummary {
  lastChange: string;
  mode: string;
  namespace?: string;
  services: DiscoveredService[];
}

export interface DomainCheckInputBody {
  connectionId: string;
  domains: string[];
}

export interface DomainDetail {
  domain: ZoneOverview;
  parent?: ZoneOverview;
  proofZone?: ZoneOverview;
  records: ManagedRecord[];
  registration?: Registration;
  subdomains: ZoneOverview[];
}

export interface DomainOffer {
  name: string;
  price?: DomainPrice;
  reason?: string;
  registrable: boolean;
  tier?: string;
}

export interface DomainOfferList {
  offers: DomainOffer[];
}

export interface DomainOperationList {
  operations: (RouteOperation | null)[];
}

export interface DomainOverview {
  registrations: (Registration | null)[];
  zones: ZoneOverview[];
}

export interface DomainPrice {
  currency: string;
  registrationCost: string;
  renewalCost: string;
}

export interface DomainRouteList {
  routes: (CloudinfraRoute | null)[];
}

export interface DrainReview {
  reservations: number;
  review: string;
  serverId: string;
  serverRevision: number;
  usage?: ServerUsage;
}

export interface DriftCounts {
  changed: number;
  deployed: number;
  missing: number;
}

export interface Edge {
  confidence: string;
  evidence: Evidence[];
  from: string;
  kind: string;
  to: string;
}

export interface EdgeInputBody {
  policy: {
  enabled: boolean;
  invalidateOnDeploy: boolean;
  rules: EdgeRule[];
};
  revision: number;
}

export interface EdgePolicy {
  enabled: boolean;
  invalidateOnDeploy: boolean;
  rules: EdgeRule[];
}

export interface EdgePurgeInputBody {
  revision: number;
  urls: string[];
}

export interface EdgeRule {
  browserTtl: number;
  edgeTtl: number;
  mode: string;
  pathPrefix: string;
}

export interface EdgeState {
  appliedAt: string;
  desired?: DNSRecord;
  invalidationError?: string;
  kind: string;
  lastDeploymentId?: string;
  lastPurgeAt: string;
  message: string;
  orgId: string;
  policy: EdgePolicy;
  prior?: DNSRecord;
  projectId: string;
  requestedAt: string;
  revision: number;
  routeId: string;
  status: string;
  urls?: string[];
}

export interface EditInputBody {
  baseRevision?: number;
  addResource?: {
  enabledIn?: string;
  gpu?: ManifestGPU;
  kind: string;
  name: string;
  provider?: string;
  settings?: Record<string, any>;
  target?: string;
  uses?: Record<string, any>;
  version?: string;
};
  ignore?: string;
  link?: {
  need: string;
  options?: Record<string, any>;
  service: string;
};
  placement?: {
  build?: Build;
  cloudSqlNetwork?: CloudSQLApplicationNetwork;
  cloudTags?: string[];
  cloudTarget?: string;
  enabled?: boolean;
  endpoint?: ManifestEndpoint;
  environment?: string;
  healthPath?: string;
  name: string;
  provider?: string;
  region?: string;
  scale?: Scale;
  section: string;
  size?: SizeEdit;
  target?: string;
};
  removeResource?: string;
  setUse?: {
  dependency: string;
  resource: string;
  use: Use;
};
  unlink?: {
  need: string;
  options?: Record<string, any>;
  service: string;
};
  updateResource?: {
  clearGpu?: boolean;
  enabled?: boolean;
  environment?: string;
  gpu?: ManifestGPU;
  provider?: string;
  resource: string;
  settings?: Record<string, any>;
  target?: string;
};
}

export interface Embeddings {
  deployment?: string;
  dimensions: number;
  mode: string;
  model: string;
  provider?: string;
}

export interface EndpointList {
  endpoints: (CatalogconnectEndpoint | null)[];
}

export interface EnrollmentBody {
  hostKey: string;
}

export interface EntryDetail {
  contextLength: number;
  cpu: boolean;
  family: string;
  fitsHere?: boolean;
  fitsNotice?: string;
  gated: boolean;
  huggingFaceUrl: string;
  id: string;
  license: LicenseInfo;
  minGpuMemoryGib: number;
  parameters: number;
  publisher: string;
  quantizations: QuantizationView[];
  queryPrefix?: string;
  revision: string;
  summary: string;
  tasks: string[];
  testedOn: string[];
  title: string;
  verification: string;
}

export interface EntrySummary {
  contextLength: number;
  cpu: boolean;
  family: string;
  fitsHere?: boolean;
  gated: boolean;
  id: string;
  license: LicenseInfo;
  minGpuMemoryGib: number;
  parameters: number;
  publisher: string;
  queryPrefix?: string;
  summary: string;
  tasks: string[];
  testedOn: string[];
  title: string;
  verification: string;
}

export interface EnvLink {
  address?: string;
  field?: string;
  from: string;
  gateway?: string;
  key: string;
  options?: Record<string, any>;
  subject: string;
}

export interface EnvVar {
  from?: Ref;
  name: string;
  requires?: string;
  secret?: string;
  template?: string;
  value?: string;
}

export interface Environment {
  createdAt: string;
  id: string;
  kind: string;
  name: string;
  orgId: string;
  projectId: string;
  updatedAt: string;
}

export interface EnvironmentChange {
  compensationMessage?: string;
  compensationMode?: string;
  deploymentId?: string;
  environment: string;
  environmentId: string;
  error?: string;
  externalRouting: boolean;
  planId?: string;
  projectId?: string;
  routes?: SyncRoute[];
  routingEvidence?: string;
  routingStatus?: string;
  services: string[];
  status: string;
  summary: PlanSummary;
}

export interface EnvironmentCloud {
  assignment: Assignment | null;
  revision: number;
}

export interface EnvironmentCloudEvent {
  cloudId: string | null;
  environmentId: string;
  orgId: string;
  projectId: string;
  revision: number;
}

export interface EnvironmentCloudInputBody {
  cloudId: string;
  revision: number;
}

export interface EnvironmentConnections {
  connections: Record<string, any>;
  environment: string;
}

export interface EnvironmentList {
  environments: Environment[];
}

export interface EnvironmentUsage {
  assigned: boolean;
  environmentId: string;
  environmentName: string;
  placements: PlacementUsage[];
  projectId: string;
  projectName: string;
}

export interface Evidence {
  detail: string;
  line?: number;
  path: string;
  source?: string;
}

export interface ExcludeInputBody {
  exclusions: {
  keepRunning?: boolean;
  kind: string;
  match: string;
  name?: string;
  reason: string;
}[];
}

export interface Excluded {
  rule: ExclusionRule;
  service: Service;
}

export interface Exclusion {
  createdAt: string;
  createdBy: string;
  keepRunning: boolean;
  kind: string;
  match: string;
  name?: string;
  reason: string;
}

export interface ExclusionRule {
  kind: string;
  match: string;
}

export interface Expansion {
  accelerator?: Accelerator;
  accountId: string;
  approvedAt?: string;
  approvedBy: string;
  architecture: string;
  block?: CapacityBlock;
  cloudId: string;
  connectionGeneration: number;
  connectionId: string;
  createdAt: string;
  estimatedHourlyUsd?: number;
  hourlyPriceKnown?: boolean;
  id: string;
  image: string;
  intentHash: string;
  minimum: boolean;
  operationId: string;
  orgId: string;
  policyRevision: number;
  poolId: string;
  provider: string;
  region: string;
  resources: ResourceVector;
  revision: number;
  size: string;
  status: string;
  template: PoolExpansionPolicy;
  updatedAt: string;
}

export interface ExpansionList {
  expansions: Expansion[];
}

export interface FabriqKeyList {
  keys: ConsumerKey[];
}

export interface FabriqloomStatus {
  entityTypes: number;
  keysReportUse: boolean;
  kind: string;
  migrations: number;
  migrationsKnown: boolean;
  reachable: boolean;
  reason?: string;
  version: string;
}

export interface FailedCleanup {
  attempt: number;
  deploymentId: string;
  error?: string;
  revision: string;
  status: string;
}

export interface FailedCleanupInputBody {
  revision: string;
}

export interface FarpConfig {
  gateway: string;
  metadata?: Record<string, any>;
  port: number;
}

export interface FarpHostnamesInputBody {
  hostnames: string[];
  revision: number;
}

export interface Features {
  ai: boolean;
}

export interface Federation {
  accessGrants?: Record<string, any>;
  destinations?: Record<string, any>;
  networkLinks?: Record<string, any>;
  placements?: Record<string, any>;
  serviceExports?: Record<string, any>;
}

export interface File {
  mode?: number;
  path: string;
}

export interface Finding {
  code: string;
  evidence?: Evidence[];
  message: string;
  name?: string;
  path?: string;
  subject?: string;
}

export interface Fit {
  gpuKind: string;
  need: Need;
  reason?: string;
  result?: Result;
  status: string;
}

export interface FrontendDemand {
  accountId: string;
  cloudId: string;
  connectionGeneration: number;
  connectionId: string;
  environmentId: string;
  grantRevision: number;
  orgId: string;
  planId: string;
  projectId: string;
  provider: string;
  service: string;
  targetId: string;
  targetRevision: number;
}

export interface FrontendLimit {
  maximum?: number;
  targetId: string;
}

export interface FrontendUsage {
  active: number;
  counted: number;
  maximum?: number;
  pending: number;
  retained: number;
  targetId: string;
}

export interface GPUDevice {
  index: number;
  kind: string;
  memoryMib: number;
  product: string;
  uuid: string;
}

export interface GPUInventory {
  checkedAt: string;
  devices?: GPUDevice[];
  message?: string;
  nodes?: GPUNodeGroup[];
  status: string;
}

export interface GPULimit {
  kind: string;
  maximum: number;
}

export interface GPUNodeGroup {
  hourlyPriceKnown: boolean;
  hourlyUsd: number;
  instanceType?: string;
  kind: string;
  memoryGib: number;
  nodeSelector?: Record<string, any>;
  nodes: number;
  perNode: number;
  product?: string;
  tolerations?: GPUToleration[];
  total: number;
}

export interface GPUPlacement {
  count: number;
  kind: string;
  nodeSelector?: Record<string, any>;
  tolerations?: GPUToleration[];
}

export interface GPURequest {
  count: number;
  kind: string;
}

export interface GPUTarget {
  accelerator: Accelerator;
  cloud: string;
  code?: string;
  connectionId: string;
  fits: boolean;
  free: number;
  hourlyPriceKnown: boolean;
  hourlyUsd: number;
  message?: string;
  name: string;
  priceEstimate: boolean;
  regions?: string[];
  sizeId?: string;
  source: string;
}

export interface GPUTargetList {
  targets: GPUTarget[];
  unreachable: UnreachableAccount[];
}

export interface GPUToleration {
  effect: string;
  key: string;
  operator: string;
  value?: string;
}

export interface GPUUsage {
  kind: string;
  limit: number | null;
  pending: number;
  provisioned: number | null;
  reserved: number;
}

export interface GatewayList {
  gateways: GatewayStatus[];
}

export interface GatewayRoute {
  env: string;
  path: string;
  stripPrefix?: boolean;
  to: string;
}

export interface GatewayStatus {
  environmentId: string;
  id: string;
  instances: InstanceStatus[];
  name: string;
  provider: string;
  region: string;
  routes: DbgatewayRouteStatus[];
  snapshotVersion: number;
}

export interface GeneratedSecret {
  bytes?: number;
  encoding?: string;
  name: string;
  prefix?: string;
}

export interface GitHubInstallation {
  account: string;
  accountType: string;
  avatarUrl: string;
  createdAt: string;
  id: string;
  manageUrl: string;
}

export interface GitHubStatus {
  configured: boolean;
  installUrl?: string;
  installations: GitHubInstallation[];
}

export interface Graph {
  buildSystems: BuildSystem[];
  edges: Edge[];
  notes: Note[];
  packages: Package[];
  resources: Resource[];
  services: Service[];
  sources?: SourceRef[];
  version: number;
  workspaces: Workspace[];
}

export interface GraphSummary {
  deployable: number;
  needsConfirmation: number;
  packages: number;
  resources: number;
  services: number;
}

export interface HTTPProbe {
  path: string;
  port: string;
}

export interface HandoffInputBody {
  destinationCloudId: string;
  revision: number;
}

export interface HandoffReleaseInputBody {
  drainSeconds: number;
}

export interface Health {
  available: boolean;
  errors: number;
  latencyMs: number;
  reason?: string;
  requests: number;
  statistic?: string;
}

export interface Held {
  sameAddress?: string;
}

export interface HeldRoute {
  key: string;
  routeIds: string[];
  service: string;
}

export interface HostCard {
  attached: boolean;
  domainRouteId?: string;
  farpHome: boolean;
  hostname: string;
  routes: BoardRoute[];
  status?: CloudinfraRouteStatus;
}

export interface HostnameUse {
  environmentId: string;
  hostname: string;
  port: number;
  projectId: string;
  projectName: string;
  routeId: string;
  runsOn: string;
  service: string;
  status: CloudinfraRouteStatus;
}

export interface HuggingFaceToken {
  account: string;
  hint: string;
  revision: number;
  setAt: string;
}

export interface HuggingFaceTokenInputBody {
  token: string;
}

export interface IdentitySession {
  orgId: string;
  permissions: string[];
  userId: string;
}

export interface Ignore {
  resources?: string[];
}

export interface InClusterInputBody {
  name: string;
  settings: Record<string, any>;
}

export interface IncludeInputBody {
  exclusions: {
  kind: string;
  match: string;
}[];
}

export interface InfrastructureAction {
  id: string;
  kind: string;
  resourceId: string;
  status: string;
}

export interface InfrastructureStatus {
  engine: string;
  providers: ProviderStatus[];
  store: string;
}

export interface InspectInputBody {
  repo: string;
  revision?: string;
}

export interface InstanceRouteStats {
  backendConns: number;
  maxConnections: number;
  pinRate: number;
  pinned: number;
  reachable: boolean;
  routeId: string;
  waitP95Ms: number;
}

export interface InstanceStatus {
  behind: boolean;
  draining: boolean;
  id: string;
  lastSeenAt: string;
  reporting: boolean;
  routes: InstanceRouteStats[];
  snapshotVersion: number;
}

export interface KeylessInputBody {
  connectionId: string;
  name: string;
  settings: Record<string, any>;
}

export interface KeylessPrepareInputBody {
  cloud: string;
}

export interface KeylessReplaceInputBody {
  settings?: Record<string, any>;
}

export interface KeylessSetup {
  audience?: string;
  connectionId: string;
  expiresAt?: string;
  issuer: string;
  steps: SetupStep[];
  subject: string;
}

export interface KeylessState {
  enabled: boolean;
  issuer: string;
  reason: string;
}

export interface KindInfo {
  components?: ComponentInfo[];
  consumerOptions?: ConsumerOption[];
  dependencies?: DependencyInfo[];
  description?: string;
  engine: boolean;
  feature?: string;
  name: string;
  outputs?: OutputInfo[];
  placement: string;
  settings?: SettingInfo[];
  targets: string[];
  title: string;
  versions: string[];
}

export interface KubernetesRule {
  apiGroups: string[];
  resources: string[];
  verbs: string[];
}

export interface Leftover {
  dataBearing: boolean;
  name: string;
  op: string;
  reason: string;
  subject: string;
}

export interface LicenseInfo {
  commercial: boolean;
  id: string;
  note?: string;
  title: string;
  url: string;
}

export interface LinkedService {
  environment: string;
  message?: string;
  reachable: boolean;
  service: string;
}

export interface Log {
  actionId: string;
  attempt: string;
  deploymentId: string;
  id: string;
  level: string;
  message: string;
  operationId: string;
  orgId: string;
  projectId: string;
  providerId: string;
  source: string;
  stream: string;
  time: string;
  truncated: boolean;
}

export interface LogPage {
  gap: boolean;
  logs: Log[];
  next: string;
}

export interface ManagedDatabaseConfig {
  accountIdentity: string;
  cloudSqlPsc?: CloudSQLSourceEndpoint;
  connectionId: string;
  profile: ManagedDatabaseProfile;
}

export interface ManagedDatabaseProfile {
  access: string;
  availability: string;
  backupRetentionUnit: string;
  backupRetentionValue: number;
  computeClass: string;
  finalBackup: string;
  finalBackupRetentionDays: number;
  id: string;
  kind: string;
  pointInTimeRecoveryDays: number;
  provider: string;
  retainAutomatedBackups: boolean;
  revision: string;
  storageEncrypted: boolean;
  storageGb: number;
  storageType: string;
  version: string;
}

export interface ManagedRecord {
  hostname?: string;
  kind: string;
  name: string;
  projectId?: string;
  projectName?: string;
  routeId?: string;
  ttl: number;
  type: string;
  value: string;
  zoneId: string;
}

export interface ManagedRegistryBackup {
  createdAt: string;
  id: string;
  size: number;
}

export interface ManagedRegistryView {
  action: string;
  backups: ManagedRegistryBackup[];
  connectionId: string;
  dnsReady: boolean;
  generation: number;
  hostname: string;
  message: string;
  phase: string;
  status: string;
}

export interface Manifest {
  databaseGateway?: DatabaseGateway;
  defaults?: Defaults;
  environments?: Record<string, any>;
  federation?: Federation;
  gateways?: Record<string, any>;
  held?: Held;
  ignore?: Ignore;
  links?: Record<string, any>;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
}

export interface ManifestDraft {
  manifest?: Manifest;
  projectId: string;
  sources: SourceRef[];
  yaml: string;
}

export interface ManifestEndpoint {
  hostPort?: number;
  hostname?: string;
  ingressClass?: string;
  tlsSecret?: string;
}

export interface ManifestGPU {
  count: number;
  kind: string;
}

export interface ModelDeploymentList {
  models: (ModelsStatus | null)[];
}

export interface ModelsDeployment {
  accruedAt: string;
  createdAt: string;
  engine: string;
  entryId: string;
  environmentId: string;
  hourlyUsdOverride?: string;
  id: string;
  message?: string;
  month: string;
  monthCostUsd: string;
  monthlyBudgetUsd?: string;
  phase: string;
  planId?: string;
  projectId: string;
  quantization: string;
  resource: string;
  revision: number;
  serverCloudId?: string;
  serverName?: string;
  serverOperationId?: string;
  updatedAt: string;
}

export interface ModelsMoveInputBody {
  maxModelLen?: number;
  quantization?: string;
  reviewServer?: boolean;
  server?: {
  cloudId: string;
  operationId: string;
  review: string;
};
  target: {
  connectionId: string;
  gpu?: GPURequest;
  provider?: string;
  region?: string;
  sizeId?: string;
  source: string;
};
}

export interface ModelsStatus {
  canEdit: boolean;
  connectionId: string;
  connectionName: string;
  cost: Cost;
  deployment?: ModelsDeployment;
  engineVersion: string;
  entry: EntrySummary;
  environments: Reach[];
  gpu?: GPURequest;
  hint?: string;
  internalUrl?: string;
  kind: string;
  linked: LinkedService[];
  maxModelLen: number;
  message?: string;
  monthToDateUsd: string;
  moveCost?: Cost;
  pin?: PinStatus;
  planId?: string;
  projectId: string;
  projectedUsd: string;
  resource: string;
  state: string;
  updateAvailable: boolean;
  verification: Fit;
}

export interface ModelsUpdateInputBody {
  hourlyUsdOverride?: string;
  monthlyBudgetUsd?: string;
  revision: number;
}

export interface MoveInput {
  destinationServerId: string;
  environmentId: string;
  poolId: string;
  projectId: string;
  review?: string;
  service: string;
}

export interface MoveReview {
  input: MoveInput;
  placement: Placement;
  review: string;
  sourceServerId: string;
}

export interface NativeCopyCapacity {
  availability: string;
  computeClass: string;
  iops?: number;
  maxStorageGb?: number;
  profileId: string;
  profileRevision: string;
  storageGb: number;
  storageType: string;
  throughputMbps?: number;
}

export interface NativeCopyCreateInputBody {
  acknowledgeCharges: boolean;
  requestId: string;
  reviewToken: string;
}

export interface NativeCopyCredentials {
  checkedAt: string;
  connectionName: string;
  copyId: string;
  database: string;
  expiresAt: string;
  password: string;
  privateServiceConnect?: boolean;
  provider: string;
  username: string;
}

export interface NativeCopyReview {
  capacity: NativeCopyCapacity;
  notice: string;
  pointId: string;
  requestId: string;
  restoreTime?: string;
  reviewToken: string;
  sourceId: string;
  target: NativeRestoreTarget;
  ttlHours: number;
  validUntil: string;
}

export interface NativeCopyReviewInputBody {
  cloudId: string;
  requestId: string;
  restoreTime?: string;
  targetId: string;
  ttlHours: number;
}

export interface NativeInventory {
  nextBefore?: string;
  points: NativePoint[];
  source: NativeSource;
}

export interface NativePITRBounds {
  earliest: string;
  latest: string;
  protocol: number;
  settingsDigest?: string;
  sourceBackup?: string;
  sourceDeletedAt?: string;
}

export interface NativePITRWindow {
  available: boolean;
  earliest?: string;
  latest?: string;
  observedAt?: string;
  pointId?: string;
  provider: string;
  sourceId: string;
  unavailableReason?: string;
}

export interface NativePoint {
  createdAt: string;
  encrypted: boolean;
  engine: string;
  expiresAt: string | null;
  id: string;
  kind: string;
  kmsKey?: string;
  lastSeenAt: string;
  name: string;
  pitr?: NativePITRBounds;
  present: boolean;
  providerId: string;
  restoreTime?: string;
  sourceId: string;
  status: string;
  version: string;
}

export interface NativeRecoveryCopies {
  copies: NativeRestore[];
  nextBefore?: string;
}

export interface NativeRecoverySources {
  nextBefore?: string;
  sources: NativeSource[];
}

export interface NativeRestore {
  cleanupBy?: string;
  cleanupRequestedAt?: string;
  createdAt: string;
  createdBy: string;
  durationMs: number;
  environmentId: string;
  error?: string;
  expiresAt: string;
  externalId?: string;
  id: string;
  orgId: string;
  physicalId?: string;
  pointId: string;
  projectId: string;
  restoreTime?: string;
  sourceId: string;
  status: string;
  target: NativeRestoreTarget;
  verifiedAt?: string;
}

export interface NativeRestoreTarget {
  accountIdentity: string;
  cloudId?: string;
  cloudRevision?: number;
  cloudSqlNetwork?: CloudSQLRecoveryNetwork;
  connectionId: string;
  grantRevision?: number;
  nativeAccount?: string;
  nativeProject?: string;
  provider: string;
  rdsNetwork?: RDSRecoveryNetwork;
  region: string;
  targetId?: string;
  targetRevision?: number;
}

export interface NativeRetentionPolicy {
  authorizedBy: string;
  createdAt: string;
  createdBy: string;
  enabled: boolean;
  id: string;
  lastError?: string;
  lastRetirementId?: string;
  nextAt: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  revision: number;
  sourceId: string;
  updatedAt: string;
}

export interface NativeRetentionPolicyState {
  configured: boolean;
  policy?: NativeRetentionPolicy;
}

export interface NativeRetirement {
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  error?: string;
  id: string;
  orgId: string;
  pointId: string;
  policy: RetentionInput;
  policyId?: string;
  policyRevision?: number;
  projectId: string;
  sourceId: string;
  status: string;
  witnessId: string;
}

export interface NativeRetirementInputBody {
  acknowledgeDeletion: boolean;
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  requestId: string;
}

export interface NativeRetirementPage {
  nextBefore?: string;
  retirements: NativeRetirement[];
}

export interface NativeSource {
  capturedAt: string;
  checkedAt?: string;
  complete: boolean;
  connectionId: string;
  environmentId: string;
  error?: string;
  id: string;
  name: string;
  observedAt?: string;
  orgId: string;
  physicalId: string;
  projectId: string;
  provider: string;
  region: string;
  state: string;
}

export interface Need {
  gpus: number;
  maxModelLen: number;
  perGpuGib: number;
  ramGib?: number;
}

export interface NewKeyBody {
  access: string;
  expiresInDays?: number;
  name: string;
}

export interface NodePoolSpec {
  autoscale: boolean;
  count: number;
  labels?: Record<string, any>;
  max: number;
  min: number;
  name: string;
  providerOptions?: ClusterProviderOptions;
  size: string;
  taints?: NodePoolTaint[];
}

export interface NodePoolTaint {
  effect: string;
  key: string;
  value: string;
}

export interface Note {
  message: string;
  path?: string;
  source?: string;
}

export interface Notification {
  actionUrl: string;
  body: string;
  createdAt: string;
  emailStatus: string;
  id: string;
  projectId: string;
  read: boolean;
  status: string;
  title: string;
}

export interface NotificationList {
  items: Notification[];
  mailEnabled: boolean;
}

export interface OK {
  ok: boolean;
}

export interface OctopusRouteInputBody {
  hostname: string;
  methods: string[];
  note?: string;
  path: string;
  port: number;
  revision?: number;
  service: string;
  stripPrefix: boolean;
}

export interface OctopusStatus {
  appliedGeneration: number;
  attach: AttachDefaults;
  farpHostnames: string[];
  generation: number;
  namespace?: string;
  privateUrl?: string;
  provider: string;
  resource: string;
  revision: number;
  startTime?: string;
  syncError?: string;
  syncedAt: string;
  version?: string;
}

export interface OctopusloomRoute {
  createdAt: string;
  createdBy: string;
  environmentId: string;
  hostname: string;
  id: string;
  methods: string[];
  note?: string;
  orgId: string;
  path: string;
  port: number;
  projectId: string;
  resource: string;
  revision: number;
  service: string;
  stripPrefix: boolean;
  updatedAt: string;
}

export interface OpenCompatModel {
  contextWindow?: number;
  dimensions?: number;
  embeddingPerMillion?: string;
  id: string;
  inputPerMillion: string;
  maxOutput?: number;
  outputPerMillion: string;
  task: string;
}

export interface Operation {
  absenceConfirmedAt?: string;
  accelerator?: Accelerator;
  accountId: string;
  actions: InfrastructureAction[];
  attempt: number;
  attempted: boolean;
  bootstrapExpiresAt: string;
  capacityCheckedAt?: string;
  capacityTarget?: ResourceVector;
  cloudId: string;
  connectionGeneration?: number;
  connectionId: string;
  connectionRevision: string;
  createdAt: string;
  enrolled: boolean;
  estimatedHourlyUsd: number;
  expiresAt: string;
  hostKey?: string;
  hourlyPriceKnown?: boolean;
  id: string;
  input: ServerInput;
  message: string;
  orgId: string;
  ownership: string;
  provider: string;
  publicIpv4: string[];
  resourceId: string;
  review: string;
  serverId: string;
  stateRevision: number;
  status: string;
  updatedAt: string;
}

export interface OperationList {
  operations: (Operation | null)[];
}

export interface OrgInvitation {
  createdAt: string;
  email: string;
  expiresAt: string;
  id: string;
  invitedBy: string;
  role: string;
}

export interface OrgInvitationList {
  invitations: OrgInvitation[];
}

export interface OrgMember {
  email: string;
  id: string;
  joinedAt: string;
  memberRole: string;
  name: string;
  role: string;
  userId: string;
}

export interface OrgMemberList {
  members: OrgMember[];
}

export interface Output {
  envKey: string;
  from?: Ref;
  name: string;
  perConsumer?: boolean;
  requires?: string;
  secret?: boolean;
  template?: string;
}

export interface OutputInfo {
  envKey: string;
  name: string;
  perConsumer: boolean;
}

export interface Package {
  ecosystem: string;
  evidence: Evidence[];
  id: string;
  name: string;
  path: string;
  requires?: Requirement[];
  runtime?: string;
  source?: string;
}

export interface PinStatus {
  file?: string;
  message?: string;
  model?: string;
  revision?: string;
  updateAvailable: boolean;
}

export interface Placement {
  allocation?: string;
  connectionGeneration: number;
  connectionId: string;
  cpuMillis: number;
  gpuKind?: string;
  gpus?: number;
  memoryBytes: number;
  poolId: string;
  poolRevision: number;
  serverId: string;
  serverRevision: number;
}

export interface PlacementUsage {
  default?: boolean;
  services: string[];
  targetId?: string;
}

export interface Plan {
  actions: Action[];
  artifacts: ArtifactChoice[];
  catalog: boolean;
  cloudSyncId?: string;
  connections: Record<string, any>;
  createdAt: string;
  createdBy: string;
  decidedAt?: string;
  decidedBy: string;
  decisionNote: string;
  environment: string;
  environmentId: string;
  error: string;
  executable: boolean;
  expiresAt?: string;
  fromEnvironment: string;
  fromEnvironmentId: string;
  id: string;
  kind: string;
  leftovers: Leftover[];
  manifestId: string;
  manifestRevision: number;
  orgId: string;
  plannedAt?: string;
  problems: Finding[];
  projectId: string;
  release?: Release;
  revision: string;
  rollback?: Rollback;
  sources: PlanSource[];
  status: string;
  summary: PlanningSummary;
  warnings: Finding[];
}

export interface PlanCounts {
  approved: number;
  expired: number;
  failed: number;
  rejected: number;
  superseded: number;
  waiting: number;
}

export interface PlanList {
  next?: string;
  plans: Plan[];
}

export interface PlanSource {
  alias: string;
  analysisId: string;
  commitSha: string;
  fromPlanId?: string;
  inScope: boolean;
  ref?: string;
  repository: string;
  root: string;
  sourceId: string;
}

export interface PlanSummary {
  adopt: number;
  create: number;
  delete: number;
  keep: number;
  replace: number;
  update: number;
}

export interface PlanningCloudList {
  clouds: (Profile | null)[];
}

export interface PlanningSummary {
  create: number;
  delete: number;
  keep: number;
  replace: number;
  update: number;
}

export interface Point {
  t: string;
  v: number;
}

export interface Policy {
  createdBy: string;
  enabled: boolean;
  environment: string;
  environmentId: string;
  everyHours: number;
  id: string;
  lastError?: string;
  nextAt: string;
  orgId: string;
  projectId: string;
  resource: string;
}

export interface PolicyInputBody {
  enabled: boolean;
  environment: string;
  everyHours: number;
  resource: string;
}

export interface PolicyList {
  policies: Policy[];
}

export interface Pool {
  acceleratorKind?: string;
  cloudId: string;
  id: string;
  members: PoolMember[];
  name: string;
  orgId: string;
  revision: number;
}

export interface PoolExpansionPolicy {
  acceleratorKind?: string;
  allowedSizes: string[];
  architecture: string;
  bootstrapSize: string;
  connectionId: string;
  image: string;
  limits?: ResourceVector;
  maxServers: number;
  minServers: number;
  poolId: string;
  region: string;
  systemReserve: ResourceVector;
}

export interface PoolInputBody {
  acceleratorKind?: string;
  members: {
  cpuMillis: number;
  draining: boolean;
  gpus?: number;
  memoryBytes: number;
  serverId: string;
}[];
  name: string;
  revision: number;
}

export interface PoolList {
  pools: (Pool | null)[];
}

export interface PoolMember {
  cpuMillis: number;
  draining: boolean;
  gpus?: number;
  memoryBytes: number;
  serverId: string;
}

export interface Port {
  container: number;
  control?: boolean;
  name: string;
  protocol: string;
  routable?: boolean;
}

export interface PreferenceInputBody {
  deploymentEmail: boolean;
}

export interface Preferences {
  deploymentEmail: boolean;
  mailEnabled: boolean;
}

export interface Preview {
  after?: Target;
  afterAssignment?: Assignment;
  afterDefault?: DefaultPlacement;
  before?: Target;
  beforeAssignment?: Assignment;
  beforeDefault?: DefaultPlacement;
  blockers: string[];
  createdAt: string;
  environments: EnvironmentChange[];
  id: string;
  input: ChangeInput;
  orgId: string;
  projectId: string;
  reverts?: string;
  revision: string;
}

export interface Problem {
  code: string;
  message: string;
}

export interface Profile {
  createdAt: string;
  defaultPlacement: DefaultPlacement | null;
  id: string;
  name: string;
  orgId: string;
  projectId: string;
  revision: number;
  updatedAt: string;
}

export interface Project {
  color: string;
  createdAt: string;
  createdBy: string;
  description: string;
  exclusions: Exclusion[];
  icon: string;
  id: string;
  name: string;
  orgId: string;
  repositories: string[];
  slug: string;
  status: string;
  updatedAt: string;
}

export interface ProjectDomain {
  hostnames: HostnameUse[];
  mode: string;
  name: string;
  provider?: string;
  verified: boolean;
  wildcards: boolean;
  zoneId: string;
}

export interface ProjectDomainList {
  domains: ProjectDomain[];
}

export interface ProjectGrant {
  cloudId: string;
  orgId: string;
  projectId: string;
  revision: number;
  revoked: boolean;
  targetIds: string[];
}

export interface ProjectList {
  projects: Project[];
}

export interface ProjectView {
  excluded: Excluded[];
  graph?: Graph;
  projectId: string;
  sources: SourceState[];
}

export interface ProjectZone {
  id: string;
  mode: string;
  name: string;
  verified: boolean;
}

export interface ProjectZoneList {
  zones: ProjectZone[];
}

export interface Provider {
  available: boolean;
  buildLogs?: string;
  cloud: string;
  description: string;
  infrastructure?: string[];
  logNote?: string;
  model: string;
  name: string;
  note?: string;
  provisions?: Provisions[];
  regions?: string[];
  runs?: Runs[];
  scaling: boolean;
  title: string;
}

export interface ProviderKey {
  createdAt: string;
  createdBy: string;
  fields?: Record<string, any>;
  id: string;
  keyHint?: string;
  models?: OpenCompatModel[];
  name: string;
  orgId: string;
  projects: string[];
  revision: number;
  type: string;
  updatedAt: string;
  updatedBy: string;
}

export interface ProviderList {
  providers: Provider[];
}

export interface ProviderStatus {
  capabilities: string[];
  name: string;
  region: string;
  version: string;
}

export interface ProviderZoneList {
  zones: DNSZone[];
}

export interface Provisions {
  creates: string;
  kind: string;
  managedProfiles?: ManagedDatabaseProfile[];
  versions?: string[];
}

export interface PublicConfig {
  appId: string;
  authBasePath: string;
  consolePath?: string;
  features: Features;
  publishableKey: string;
  setupPending: boolean;
  waitlist: boolean;
}

export interface QuantizationView {
  args?: string[];
  engine: string;
  file?: string;
  fits: Fit[];
  id: string;
  minGpuMemoryGib?: number;
  minMemoryGib?: number;
  repo?: string;
  revision?: string;
  weightBytes: number;
}

export interface RDSRecoveryNetwork {
  securityGroupId: string;
  subnetGroup: string;
  verifierGroupId: string;
  vpcId: string;
}

export interface Reach {
  environment: string;
  environmentId: string;
  message?: string;
  mode: string;
}

export interface RealtimeForgeGap {
  reason: string;
}

export interface RealtimeForgeResumed {
  count: number;
  from: string;
}

export interface RealtimeRealtimeSnapshot {
  cursor: string;
  partial: boolean;
  records: SnapshotRecord[];
}

export interface RealtimeRealtimeStatus {
  cursor: string;
  time: string;
}

export interface RealtimeReceive {
  channel: string;
  cursor: string;
  data: any;
  event: string;
}

export interface RealtimeSend {
  cursor?: string;
  deploymentIds?: string[];
  op: string;
  orgId: string;
  projectIds?: string[];
}

export interface Rebuild {
  createdAt: string;
  createdBy: string;
  finishedAt?: string;
  id: string;
  output: string;
  projection: string;
  retryable: boolean;
  status: string;
}

export interface RebuildInputBody {
  projection: string;
  requestId: string;
}

export interface RebuildList {
  rebuilds: Rebuild[];
}

export interface Recipe {
  architecture: string;
  backupFormat: string;
  extensions?: string[];
  family?: string;
  fsGroup?: number;
  id: string;
  image: string;
  mountPath: string;
  productVersion: string;
  revision: string;
  volumeSizeMb?: number;
}

export interface RecipesGPU {
  count: number;
  kind?: string;
  minMemoryGib?: number;
}

export interface RecoveryInputBody {
  planId: string;
  review?: string;
  service: string;
}

export interface Ref {
  component?: string;
  dependency?: string;
  field: string;
}

export interface Registration {
  accountId: string;
  attempt: number;
  attempted: boolean;
  autoRenew: boolean;
  connectionId: string;
  createdAt: string;
  customRegistrant: boolean;
  domainExpiresAt: string;
  firstUnconfirmedAt: string;
  id: string;
  message: string;
  name: string;
  noticeSentFor: string;
  orgId: string;
  privacyMode: string;
  projectIds: string[];
  provider: string;
  quote: DomainPrice;
  refreshedAt: string;
  resultNotifiedFor: string;
  review: string;
  reviewExpiresAt: string;
  revision: number;
  sandbox: boolean;
  status: string;
  updatedAt: string;
  zoneId: string;
}

export interface RegistrationApprovalBody {
  currency: string;
  registrationCost: string;
  review: string;
}

export interface RegistrationExtensionList {
  extensions: string[];
}

export interface RegistrationInputBody {
  autoRenew?: boolean;
  connectionId: string;
  name: string;
  privacyMode?: string;
  projectIds?: string[];
  registrant?: {
  city: string;
  countryCode: string;
  email: string;
  name: string;
  organization: string;
  phone: string;
  postalCode: string;
  state: string;
  street: string;
};
}

export interface RegistrationList {
  registrations: (Registration | null)[];
}

export interface RegistryActionInputBody {
  backupId?: string;
  deleteData?: boolean;
  generation: number;
  hostname: string;
  kind: string;
  removeSetup?: boolean;
}

export interface RegistryList {
  counts: Record<string, any>;
  entries: EntrySummary[];
  fitsNotice?: string;
}

export interface Release {
  canary?: string[];
  holdSeconds?: number;
  steps?: number[];
  strategy: string;
  thresholds: Thresholds;
}

export interface ReleaseEvidence {
  deploymentId: string;
  environment: string;
  reason?: string;
  settled: boolean;
  status?: string;
  unverified: boolean;
  verdict?: string;
  watchId?: string;
  watched: boolean;
}

export interface ReleaseRef {
  deploymentId: string;
  finishedAt: string;
  kind: string;
  planId: string;
  status: string;
}

export interface RemoveRouteInputBody {
  revision: number;
}

export interface ReplaceInputBody {
  credential: string;
  settings?: Record<string, any>;
}

export interface Requirement {
  evidence: Evidence;
  git?: string;
  name: string;
  version?: string;
}

export interface ReservationRecovery {
  active: boolean;
  phase: string;
  planId: string;
  review: string;
  revision: number;
  serverId: string;
  service: string;
  started: boolean;
  storageRetained: boolean;
}

export interface Resolution {
  environment: string;
  projectId: string;
  service?: string;
  variables: ResolvedVariable[];
}

export interface ResolvedComponent {
  after?: string[];
  args?: string[];
  command?: string[];
  env?: EnvVar[];
  files?: File[];
  image: string;
  kubernetesRules?: KubernetesRule[];
  name: string;
  ports?: Port[];
  protection?: string;
  readiness?: HTTPProbe;
  requires?: string;
  resources: Resources;
  role: string;
  startTimeout?: number;
  step: number;
  timeout?: number;
  volume?: Volume;
  weightCache?: WeightCache;
}

export interface ResolvedVariable {
  key: string;
  overrides: Variable[];
  variable: Variable;
}

export interface Resource {
  confidence: string;
  evidence: Evidence[];
  id: string;
  kind: string;
  needsConfirmation: boolean;
}

export interface ResourceConfig {
  catalog?: Recipe;
  cloudPlacement?: CloudPlacement;
  component?: ComponentConfig;
  composition?: CompositionConfig;
  kind: string;
  managed?: ManagedDatabaseConfig;
  provider: string;
  region?: string;
  routablePorts?: number[];
  target?: string;
  targetSettings?: Record<string, any>;
  version?: string;
}

export interface ResourceStep {
  cpuMillis?: number;
  memoryMb?: number;
  shmMb?: number;
}

export interface ResourceVector {
  cpuMillis: number;
  gpuKind?: string;
  gpus?: number;
  memoryBytes: number;
  servers: number;
  storageBytes: number;
}

export interface Resources {
  cpuMillis: number;
  gpu?: RecipesGPU;
  memoryMb: number;
  perGpu?: ResourceStep;
  shmMb?: number;
}

export interface Restore {
  backupId: string;
  createdAt: string;
  createdBy: string;
  durationMs: number;
  error?: string;
  expiresAt: string;
  externalId?: string;
  id: string;
  orgId: string;
  projectId: string;
  status: string;
  verifiedAt?: string;
}

export interface RestoreInputBody {
  requestId: string;
}

export interface RestoreList {
  restores: Restore[];
}

export interface Result {
  detail: string;
  engine: string;
  engineVersion: string;
  entry: string;
  gpuKind: string;
  gpus: number;
  maxModelLen: number;
  outcome: string;
  quantization: string;
  ranAt: string;
  revision: string;
  run: string;
}

export interface RetainedPoolStorage {
  environmentId: string;
  planId: string;
  projectId: string;
  service: string;
}

export interface RetentionInput {
  keepLast: number;
  olderThanDays: number;
}

export interface RetentionPoint {
  backupId: string;
  bytes: number;
  createdAt: string;
  deletionSupported: boolean;
  deletionUnavailableReason?: string;
  eligible: boolean;
  reason: string;
}

export interface RetentionPolicy {
  anchorId: string;
  authorizedBy: string;
  createdAt: string;
  createdBy: string;
  enabled: boolean;
  id: string;
  lastError?: string;
  lastRetirementId?: string;
  nextAt: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  revision: number;
  updatedAt: string;
}

export interface RetentionPolicyHistory {
  events: RetentionPolicyRevision[];
  nextBefore?: string;
}

export interface RetentionPolicyInputBody {
  enabled: boolean;
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  revision: number;
}

export interface RetentionPolicyRevision {
  authorizedBy: string;
  createdAt: string;
  enabled: boolean;
  policy: RetentionInput;
  revision: number;
}

export interface RetentionPolicyState {
  configured: boolean;
  policy?: RetentionPolicy;
}

export interface RetentionPreview {
  asOf: string;
  complete: boolean;
  eligibleBytes: number;
  eligibleCount: number;
  points: RetentionPoint[];
  verifiedBackupId?: string;
}

export interface Retirement {
  attempt: number;
  attemptLimit: number;
  backupId: string;
  completedAt?: string;
  createdAt: string;
  createdBy: string;
  error?: string;
  executionBy: string;
  id: string;
  orgId: string;
  policy: RetentionInput;
  projectId: string;
  retentionPolicyId?: string;
  retentionRevision?: number;
  status: string;
  witnessId: string;
}

export interface RetirementEvent {
  actor: string;
  attempt: number;
  attemptLimit: number;
  createdAt: string;
  cursor: string;
  error?: string;
  kind: string;
  previousActor?: string;
  previousError?: string;
  status: string;
}

export interface RetirementHistory {
  events: RetirementEvent[];
  nextBefore?: string;
}

export interface RetirementInputBody {
  policy: {
  keepLast: number;
  olderThanDays: number;
};
  requestId: string;
}

export interface RetirementPage {
  nextBefore?: string;
  retirements: Retirement[];
}

export interface RetirementRetryInputBody {
  requestId: string;
}

export interface ReusedArtifact {
  architecture?: string;
  commitSha: string;
  digest?: string;
  fromDeploymentId: string;
  fromEnvironment: string;
  reference?: string;
  revision?: string;
}

export interface Revision {
  createdAt: string;
  createdBy: string;
  digest: string;
  id: string;
  manifest?: Manifest;
  orgId: string;
  projectId: string;
  revision: number;
  yaml: string;
}

export interface RevisionList {
  revisions: Revision[];
}

export interface Rollback {
  reason: string;
  restoreDeploymentId: string;
  restoreFinishedAt: string;
  restorePlanId: string;
  targetDeploymentId: string;
  targetPlanId: string;
  trigger: string;
  watchId?: string;
}

export interface RollbackCommit {
  alias: string;
  from: string;
  to: string;
}

export interface RollbackInputBody {
  deploymentId?: string;
  environment: string;
  reason: string;
}

export interface RollbackTarget {
  commits: RollbackCommit[];
  environment: string;
  restore: ReleaseRef;
  target: ReleaseRef;
}

export interface RouteBinding {
  address: string;
  backend?: string;
  bindingId: string;
  configDigest: string;
  connectionGeneration: number;
  connectionId: string;
  containerId: string;
  externalId?: string;
  ingressClass?: string;
  instanceId: string;
  namespace?: string;
  provider?: string;
  releaseId: string;
  runtimeId: string;
  serverId: string;
  serviceName?: string;
  targetId: string;
}

export interface RouteCost {
  estimate: boolean;
  monthlyUsd: string;
  source: string;
}

export interface RouteInput {
  blockedPathPrefixes?: string[];
  cloudId: string;
  environmentId: string;
  externalTtlSeconds?: number;
  hostname: string;
  port: number;
  service: string;
  zoneId: string;
}

export interface RouteInputBody {
  blockedPathPrefixes?: string[];
  cloudId: string;
  environmentId: string;
  externalTtlSeconds?: number;
  hostname: string;
  port: number;
  service: string;
  zoneId: string;
}

export interface RouteOperation {
  attempt: number;
  cost?: RouteCost;
  createdAt: string;
  desiredRecord: DNSRecord;
  desiredRecords?: DNSRecord[];
  dnsAttempted: boolean;
  expiresAt: string;
  generation: number;
  handoffId?: string;
  id: string;
  kind: string;
  message: string;
  notBefore: string;
  notices?: string[];
  previousReceipt?: DNSRecord;
  receipt?: DNSRecord;
  receipts?: DNSRecord[];
  review: string;
  revision: number;
  route: CloudinfraRoute;
  source?: CloudinfraRoute;
  status: string;
}

export interface RouteResult {
  route?: OctopusloomRoute;
  sync: SyncResult;
}

export interface RouteRevisionInputBody {
  revision: number;
}

export interface RouteSummary {
  gatewayId: string;
  id: string;
  resource: string;
  state: string;
}

export interface RouteTTLInputBody {
  externalTtlSeconds: number;
  revision: number;
}

export interface RouteVerification {
  backend: boolean;
  backendError?: string;
  certExpires: string;
  certState?: string;
  checkedAt: string;
  dns: boolean;
  dnsError?: string;
  tls: boolean;
  tlsError?: string;
}

export interface Runs {
  creates: string;
  execution: string;
  workload: string;
}

export interface RuntimeHealth {
  architecture: string;
  checkedAt: string;
  cpus: number;
  dockerId: string;
  effectiveCpuMillis: number;
  effectiveMemoryBytes: number;
  freeStorageBytes?: number;
  gpuMessage?: string;
  gpuRuntime?: string;
  gpus?: GPUDevice[];
  memoryBytes: number;
  storageBytes?: number;
}

export interface SaveInputBody {
  baseRevision?: number;
  manifest?: {
  databaseGateway?: DatabaseGateway;
  defaults?: Defaults;
  environments?: Record<string, any>;
  federation?: Federation;
  gateways?: Record<string, any>;
  held?: Held;
  ignore?: Ignore;
  links?: Record<string, any>;
  resources?: Record<string, any>;
  services?: Record<string, any>;
  version: number;
};
  yaml?: string;
}

export interface Scale {
  max: number;
  min: number;
}

export interface ScalingPool {
  policy?: CapacityPolicy;
  pool?: Pool;
}

export interface ScalingPoolInputBody {
  acceleratorKind?: string;
  expectedRevision: number;
  name: string;
  policy: {
  cloudId: string;
  enabled: boolean;
  frontendLimits: FrontendLimit[];
  gpuLimits?: GPULimit[];
  limits: ResourceVector;
  orgId: string;
  pools: PoolExpansionPolicy[];
  requireExpansionApproval: boolean;
  revision: number;
};
  template: {
  acceleratorKind?: string;
  allowedSizes: string[];
  architecture: string;
  bootstrapSize: string;
  connectionId: string;
  image: string;
  limits?: ResourceVector;
  maxServers: number;
  minServers: number;
  poolId: string;
  region: string;
  systemReserve: ResourceVector;
};
}

export interface Server {
  accelerator?: Accelerator;
  accountId: string;
  cloudId: string;
  connectionId: string;
  createdAt: string;
  creationOperationId: string;
  draining: boolean;
  health: RuntimeHealth;
  hourlyPriceKnown?: boolean;
  hourlyUsd?: number;
  id: string;
  name: string;
  orgId: string;
  ownership: string;
  pricedSize?: string;
  provider: string;
  region: string;
  resourceId: string;
  revision: number;
  runtimeConnectionId: string;
  size: string;
  stateRevision: number;
  status: string;
  updatedAt: string;
}

export interface ServerImage {
  accelerators?: string[];
  architecture?: string;
  bootstrap: boolean;
  id: string;
  minDiskGb?: number;
  name: string;
  regions: string[];
}

export interface ServerInput {
  cloudRevision: number;
  confirmAdoption?: boolean;
  connectionId?: string;
  image?: string;
  kind: string;
  name?: string;
  region?: string;
  serverId?: string;
  serverRevision?: number;
  size?: string;
}

export interface ServerInputBody {
  cloudRevision: number;
  confirmAdoption?: boolean;
  connectionId?: string;
  image?: string;
  kind: string;
  name?: string;
  region?: string;
  serverId?: string;
  serverRevision?: number;
  size?: string;
}

export interface ServerList {
  servers: (Server | null)[];
}

export interface ServerOptions {
  images: ServerImage[];
  regions: ServerRegion[];
  sizes: ServerSize[];
}

export interface ServerRegion {
  id: string;
  name: string;
}

export interface ServerSize {
  accelerator?: Accelerator;
  architectures?: string[];
  cpus: number;
  diskGb: number;
  hourlyPriceKnown?: boolean;
  hourlyUsd: number;
  id: string;
  memoryMib: number;
  monthlyUsd: number;
  regions: string[];
}

export interface ServerUsage {
  draining: boolean;
  reservedCpuMillis: number;
  reservedGpus: number;
  reservedMemoryBytes: number;
  retainedStorage: RetainedPoolStorage[];
  serverId: string;
  workloads: ServerWorkload[];
}

export interface ServerWorkload {
  active: boolean;
  bindingId?: string;
  cpuMillis: number;
  deploymentId?: string;
  environmentId: string;
  executionRevision: number;
  gpus: number;
  memoryBytes: number;
  phase: string;
  planId: string;
  poolId: string;
  projectId: string;
  service: string;
}

export interface Service {
  commands: Commands;
  confidence: string;
  context?: string;
  deployable: boolean;
  ecosystem: string;
  entrypoint: string;
  evidence: Evidence[];
  framework?: string;
  id: string;
  image?: string;
  kind: string;
  name: string;
  path: string;
  ports?: number[];
  requires?: Requirement[];
  runtime?: string;
  source?: string;
  unit?: string;
  unitName?: string;
}

export interface ServiceConfig {
  appliedInputs?: AppliedInputs;
  artifact?: ReusedArtifact;
  build?: string;
  buildTarget?: BuildTarget;
  capacityIntent?: CapacityIntent;
  catalog?: Recipe;
  cloudPlacement?: CloudPlacement;
  cloudSqlNetwork?: CloudSQLApplicationNetwork;
  commitSha?: string;
  component?: ComponentConfig;
  context?: string;
  databaseGateway?: DatabaseGatewayConfig;
  ecosystem: string;
  endpoint?: ManifestEndpoint;
  env?: EnvLink[];
  execution: string;
  farp?: FarpConfig;
  framework?: string;
  frontendDemand?: FrontendDemand;
  gateway?: GatewayRoute[];
  gpu?: GPUPlacement;
  healthPath?: string;
  image?: string;
  internal?: boolean;
  port?: number;
  privateEgress?: boolean;
  provider: string;
  public: boolean;
  region?: string;
  repo?: string;
  scale?: Scale;
  service: string;
  size?: Size;
  source: string;
  start?: string;
  target?: string;
  targetSettings?: Record<string, any>;
  workload: string;
}

export interface ServiceCoverage {
  name: string;
  provider: string;
  reason?: string;
  watchable: boolean;
}

export interface ServicePort {
  port: number;
  service: string;
}

export interface ServiceTraffic {
  bindingId: string;
  environmentId: string;
  latency: number;
  name: string;
  previousLatency: number;
  previousRequests: number;
  projectId: string;
  provider: string;
  requests: number;
  statistic?: string;
}

export interface SetDefaultPlacementInputBody {
  defaultPlacement: ({
  connectionId: string;
  provider: string;
  region: string;
}) | null;
  revision: number;
}

export interface Setting {
  key: string;
  label: string;
  required?: boolean;
}

export interface SettingInfo {
  default?: string;
  name: string;
  pattern: string;
  required: boolean;
  title: string;
}

export interface SetupRequest {
  email: string;
  name: string;
  organizationName: string;
  password: string;
}

export interface SetupResponse {
  ok: boolean;
  verificationRequired: boolean;
}

export interface SetupStatus {
  pending: boolean;
}

export interface SetupStep {
  body?: string;
  command?: string;
  title: string;
}

export interface SignIn {
  authorizeUrl: string;
  state: string;
}

export interface SignInApp {
  clientId?: string;
  cloud: string;
  problem?: string;
  slug?: string;
  source: string;
  updatedAt?: string;
  updatedBy?: string;
}

export interface SignInAppList {
  apps: SignInApp[];
  callbackUrl: string;
  keyless: KeylessState;
}

export interface SignInInputBody {
  cloud: string;
  connectionId?: string;
  name: string;
  settings?: Record<string, any>;
}

export interface Size {
  cpu: number;
  gpu?: ManifestGPU;
  memory: number;
  shm?: number;
}

export interface SizeEdit {
  cpu: number;
  memory: number;
}

export interface Snapshot {
  cursor: string;
  partial: boolean;
  records: SnapshotRecord[];
}

export interface SnapshotRecord {
  data: any;
  entityId: string;
  event: string;
  id: string;
  orgId: string;
  projectId?: string;
  time: string;
}

export interface Soak {
  duration: number;
  environment: string;
  healthy: boolean;
  reasons: string[];
  running: boolean;
  services: SoakService[];
  since: string;
  unverified: boolean;
}

export interface SoakService {
  name: string;
  provider: string;
  reason: string;
  verdict: string;
}

export interface Source {
  alias: string;
  createdAt: string;
  createdBy: string;
  id: string;
  orgId: string;
  projectId: string;
  ref: string;
  removedAt?: string;
  repository: string;
  root: string;
  updatedAt: string;
}

export interface SourceArtifact {
  alias: string;
  commitSha: string;
  key: string;
  repository: string;
}

export interface SourceCounts {
  all: number;
  farp: number;
  here: number;
}

export interface SourceList {
  sources: DiscoverySource[];
}

export interface SourceRef {
  alias: string;
  analysisId: string;
  commitSha: string;
  repository: string;
  root?: string;
}

export interface SourceState {
  latest?: Analysis;
  shown?: Analysis;
  source: DiscoverySource;
}

export interface SpendReport {
  accounts: AccountSpend[];
  month: string;
  sources: UsageSource[];
  totals: Amount[];
}

export interface StartAnalysisInputBody {
  ref?: string;
  sources?: string[];
}

export interface Step {
  actionId: string;
  finishedAt?: string;
  image: string;
  message: string;
  name: string;
  op: string;
  order: number;
  provider: string;
  revision?: string;
  startedAt?: string;
  status: string;
  subject: string;
  url: string;
  warnings?: string[];
}

export interface StorageVolumeList {
  volumes: StoredVolume[];
}

export interface StoredVolume {
  capacityBytes: number;
  connectionId: string;
  environmentId: string;
  id: string;
  instanceId: string;
  name: string;
  projectId: string;
  provider: string;
  role: string;
  status: string;
  storageClass?: string;
}

export interface Sync {
  after?: Target;
  afterDefault?: DefaultPlacement;
  attempt: number;
  before?: Target;
  beforeDefault?: DefaultPlacement;
  cloudId: string;
  createdAt: string;
  createdBy: string;
  default?: boolean;
  environments: EnvironmentChange[];
  error?: string;
  id: string;
  orgId: string;
  previewId: string;
  projectId: string;
  reverts?: string;
  status: string;
  targetId?: string;
  updatedAt: string;
}

export interface SyncResult {
  generation: number;
  message?: string;
  restarted: boolean;
  restored: number;
  state: string;
}

export interface SyncRoute {
  cutoverId?: string;
  hostname: string;
  message?: string;
  notBefore: string;
  prepareId: string;
  recordType?: string;
  releaseId?: string;
  routeId: string;
  status: string;
  ttl?: number;
  value?: string;
}

export interface Target {
  cloudId: string;
  connectionId: string;
  createdAt: string;
  displayName: string;
  id: string;
  locationId?: string;
  managedResourceId?: string;
  name: string;
  orgId: string;
  poolId?: string;
  projectId: string;
  provider: string;
  region: string;
  revision: number;
  runtime: string;
  tags: string[];
  updatedAt: string;
}

export interface TargetChoice {
  connectionId: string;
  gpu?: GPURequest;
  provider?: string;
  region?: string;
  sizeId?: string;
  source: string;
}

export interface Tenant {
  createdAt: string;
  id: string;
  label: string;
}

export interface TenantInputBody {
  tenantId: string;
}

export interface TenantList {
  tenants: Tenant[];
}

export interface Thresholds {
  errorRate: number;
  errorRateDelta: number;
  minRequests: number;
  p95LatencyRatio: number;
}

export interface TrafficReport {
  end: string;
  previousTotal: number;
  range: string;
  requests: Point[];
  services: ServiceTraffic[];
  sources: UsageSource[];
  start: string;
  step: number;
  total: number;
}

export interface UnreachableAccount {
  connectionId: string;
  message: string;
  name: string;
}

export interface UnroutableService {
  reason: string;
  service: string;
}

export interface UnwatchedService {
  name: string;
  provider: string;
  reason: string;
}

export interface UpdateProjectInputBody {
  color?: string;
  description?: string;
  icon?: string;
  name?: string;
}

export interface UpdateSourceInputBody {
  ref?: string;
  root?: string;
}

export interface UpdateTargetInputBody {
  displayName: string;
  revision: number;
  tags?: string[];
}

export interface UpdateVariableInputBody {
  secret?: boolean;
  value: string;
}

export interface Usage {
  cloudId: string;
  environments: EnvironmentUsage[];
}

export interface UsageSource {
  cloud: string;
  connectionId: string;
  name: string;
  reason?: string;
  services?: string[];
  status: string;
}

export interface Use {
  enabled?: boolean;
  external?: string;
  resource?: string;
}

export interface Variable {
  createdAt: string;
  createdBy: string;
  environment: string;
  id: string;
  key: string;
  orgId: string;
  projectId?: string;
  scope: string;
  secret: boolean;
  service?: string;
  updatedAt: string;
  updatedBy: string;
  value: string;
}

export interface VariableList {
  variables: Variable[];
}

export interface View {
  allocatedUsd: string;
  allocations: (Allocation | null)[];
  canManage: boolean;
  embeddings: Embeddings;
  huggingface?: HuggingFaceToken;
  monthlyBudgetUsd: string;
  orgId: string;
  providerKeys: (ProviderKey | null)[];
  revision: number;
  unallocatedUsd: string;
  updatedAt: string;
  updatedBy?: string;
}

export interface Volume {
  fsGroup?: number;
  kubernetesSizeMb?: number;
  mountPath: string;
}

export interface Watch {
  boundAt: string;
  checkedAt?: string;
  createdAt: string;
  deploymentId: string;
  endsAt: string;
  environment: string;
  environmentId: string;
  held: HeldRoute[];
  heldReleased: boolean;
  id: string;
  orgId: string;
  planId: string;
  projectId: string;
  reason: string;
  rollbackApprovedAt?: string;
  rollbackPlanId: string;
  services: WatchedService[];
  settledAt?: string;
  startsAt: string;
  status: string;
  thresholds: Thresholds;
  unverified: boolean;
  unwatched: UnwatchedService[];
  updatedAt: string;
  verdict: string;
}

export interface WatchedService {
  bindingId: string;
  checkedAt?: string;
  name: string;
  op: string;
  provider: string;
  reason: string;
  revision: string;
  verdict: string;
}

export interface WebhookEndpoint {
  createdAt: string;
  description: string;
  enabled: boolean;
  id: string;
  url: string;
}

export interface WebhookEndpointInputBody {
  description: string;
  url: string;
}

export interface WebhookEndpointList {
  items: WebhookEndpoint[];
}

export interface WebhookEndpointSecret {
  endpoint: WebhookEndpoint;
  secret: string;
}

export interface WeightCache {
  kubernetesSizeMb: number;
  mountPath: string;
  sizeSetting?: string;
}

export interface Workspace {
  ecosystem: string;
  evidence: Evidence[];
  members: string[];
  path: string;
  source?: string;
  tool: string;
}

export interface Zone {
  accountId?: string;
  challengeConnectionId?: string;
  connectionGeneration?: number;
  connectionId?: string;
  id: string;
  mode: string;
  name: string;
  orgId: string;
  projectIds: string[];
  proofCheckedAt: string;
  proofExpiresAt: string;
  proofMissingAt: string;
  proofName?: string;
  proofPublishAttempted?: boolean;
  proofPublishAttemptedAt: string;
  proofPublishError?: string;
  proofRecord?: DNSRecord;
  proofValue?: string;
  provider?: string;
  providerZone: DNSZone;
  recordOwnership: boolean;
  registrationId?: string;
  revision: number;
  verifiedAt: string;
}

export interface ZoneInputBody {
  connectionId?: string;
  mode: string;
  name: string;
  projectIds: string[];
}

export interface ZoneList {
  zones: (Zone | null)[];
}

export interface ZoneOverview {
  hostnames: HostnameUse[];
  parentZoneId?: string;
  proofZoneId?: string;
  registrationId?: string;
  state: string;
  subdomains: string[];
  wildcards: boolean;
  zone?: Zone;
}

export interface ZoneUpdateBody {
  projectIds: string[];
  renewProof: boolean;
  revision: number;
}

export interface AuthConfig {
  bearerToken?: string;
  apiKey?: string;
  customHeaders?: Record<string, string>;
}

export interface ClientConfig {
  baseURL: string;
  auth?: AuthConfig;
  timeout?: number;
}

