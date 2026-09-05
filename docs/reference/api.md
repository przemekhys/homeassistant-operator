# API Reference

*Reference — every field of every custom resource, generated from the Go types. Look things up here; it does not teach.*

## Packages
- [ha.homeassistant.io/v1](#hahomeassistantiov1)
- [ha.homeassistant.io/v1alpha1](#hahomeassistantiov1alpha1)


## ha.homeassistant.io/v1

Package v1 contains API Schema definitions for the ha v1 API group.

### Resource Types
- [HomeAssistant](#homeassistant)
- [HomeAssistantArea](#homeassistantarea)
- [HomeAssistantAreaList](#homeassistantarealist)
- [HomeAssistantAutomation](#homeassistantautomation)
- [HomeAssistantAutomationList](#homeassistantautomationlist)
- [HomeAssistantConfiguration](#homeassistantconfiguration)
- [HomeAssistantConfigurationList](#homeassistantconfigurationlist)
- [HomeAssistantFloor](#homeassistantfloor)
- [HomeAssistantFloorList](#homeassistantfloorlist)
- [HomeAssistantIntegration](#homeassistantintegration)
- [HomeAssistantIntegrationList](#homeassistantintegrationlist)
- [HomeAssistantLabel](#homeassistantlabel)
- [HomeAssistantLabelList](#homeassistantlabellist)
- [HomeAssistantList](#homeassistantlist)
- [HomeAssistantScene](#homeassistantscene)
- [HomeAssistantSceneList](#homeassistantscenelist)
- [HomeAssistantScript](#homeassistantscript)
- [HomeAssistantScriptList](#homeassistantscriptlist)
- [HomeAssistantSecrets](#homeassistantsecrets)
- [HomeAssistantSecretsList](#homeassistantsecretslist)



#### AdditionalVolumesSpec



AdditionalVolumesSpec defines additional volumes to mount in the Home Assistant pod.



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `volumes` _[Volume](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#volume-v1-core) array_ | Volumes to attach to each Home Assistant pod |  | Optional: \{\} <br /> |
| `volumeMounts` _[VolumeMount](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#volumemount-v1-core) array_ | VolumeMounts to attach to each Home Assistant container |  | Optional: \{\} <br /> |


#### AlphaSpec



AlphaSpec groups experimental fields that are not yet stable enough for the
top-level spec. See spec.alpha.* lifecycle: alpha (opt-in,
default false) -> stable default false -> stable default true -> mandatory.



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `networkPolicy` _[NetworkPolicyAlphaSpec](#networkpolicyalphaspec)_ | NetworkPolicy controls whether the operator creates a NetworkPolicy<br />restricting ingress to the Home Assistant pod. |  | Optional: \{\} <br /> |
| `devices` _[DevicePassthroughEntry](#devicepassthroughentry) array_ | Devices declares host device nodes (e.g. /dev/ttyACM0 for a Zigbee/<br />Z-Wave USB coordinator) to mount into the Home Assistant container.<br />Each entry is mounted via a hostPath volume typed as a character<br />device; the container is never granted `privileged: true` for this.<br />Declaring at least one entry changes the pod's security context, so<br />this starts in spec.alpha until it stabilizes. This does not affect<br />where the pod is scheduled — the declared device(s) must already<br />exist on whichever node the pod lands on (see node pinning, a<br />separate capability) for this to be useful. |  | Optional: \{\} <br /> |


#### AutomationAction



AutomationAction defines an action to be executed by the automation
This is a flexible structure that accepts any valid Home Assistant action configuration



_Appears in:_
- [HomeAssistantAutomationSpec](#homeassistantautomationspec)



#### AutomationCondition



AutomationCondition defines a condition that must be met for the automation to execute
This is a flexible structure that accepts any valid Home Assistant condition configuration



_Appears in:_
- [HomeAssistantAutomationSpec](#homeassistantautomationspec)



#### AutomationMode

_Underlying type:_ _string_

AutomationMode defines how the automation should behave when triggered again while already running

_Validation:_
- Enum: [single restart queued parallel]

_Appears in:_
- [HomeAssistantAutomationSpec](#homeassistantautomationspec)

| Field | Description |
| --- | --- |
| `single` | AutomationModeSingle - Do not start a new run, issue a warning<br /> |
| `restart` | AutomationModeRestart - Stop previous runs and start a new one<br /> |
| `queued` | AutomationModeQueued - Queue runs in order, sequential execution guaranteed<br /> |
| `parallel` | AutomationModeParallel - Launch independent concurrent runs<br /> |


#### AutomationTrigger



AutomationTrigger defines an event that will trigger the automation
This is a flexible structure that accepts any valid Home Assistant trigger configuration



_Appears in:_
- [HomeAssistantAutomationSpec](#homeassistantautomationspec)



#### BackupSpec



BackupSpec configures Home Assistant's built-in backup system via WebSocket API.



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether automatic backups are configured in HA | false | Optional: \{\} <br /> |
| `recurrence` _string_ | Recurrence defines how often to create a backup | daily | Enum: [daily mon tue wed thu fri sat sun never] <br />Optional: \{\} <br /> |
| `time` _string_ | Time is the time of day to create the backup in HH:MM:SS 24-hour format (e.g. "03:00:00").<br />If empty, Home Assistant picks automatically. |  | Pattern: `^([01]\d\|2[0-3]):[0-5]\d:[0-5]\d$` <br />Optional: \{\} <br /> |
| `retentionCopies` _integer_ | RetentionCopies is the number of backup copies to keep. Nil means unlimited. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `retentionDays` _integer_ | RetentionDays is the number of days to keep backups. Nil means unlimited. |  | Minimum: 1 <br />Optional: \{\} <br /> |
| `includeDatabase` _boolean_ | IncludeDatabase controls whether the database is included in the backup. | true | Optional: \{\} <br /> |
| `agentIDs` _string array_ | AgentIDs is the list of backup agent IDs to use<br />(e.g. "backup.local", "google_drive.my_drive").<br />Defaults to ["backup.local"] if not specified. |  | Optional: \{\} <br /> |


#### BootstrapCredentials



BootstrapCredentials references a Secret containing admin credentials



_Appears in:_
- [BootstrapSpec](#bootstrapspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `secretRef` _[CredentialsSecretRef](#credentialssecretref)_ | SecretRef references a Secret containing username and password |  |  |


#### BootstrapSpec



BootstrapSpec configures automatic Home Assistant onboarding and API token creation



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether automatic bootstrap is performed | false | Optional: \{\} <br /> |
| `credentials` _[BootstrapCredentials](#bootstrapcredentials)_ | Credentials references a Secret containing username and password for the admin user<br />The Secret must have "username" and "password" keys |  |  |
| `createApiToken` _boolean_ | CreateAPIToken controls whether a long-lived access token is created after onboarding<br />The token is valid for 10 years and stored in a Secret | true | Optional: \{\} <br /> |
| `apiTokenSecretName` _string_ | APITokenSecretName is the name of the Secret where the API token will be stored<br />The Secret will have a "token" key containing the long-lived access token<br />If not specified, defaults to "\{homeassistant-name\}-homeassistant-api-token" |  | Optional: \{\} <br /> |
| `ownerName` _string_ | OwnerName is the display name for the owner user created during onboarding | Admin | Optional: \{\} <br /> |
| `language` _string_ | Language is the language code for Home Assistant (e.g., "en", "pl") | en | Optional: \{\} <br /> |
| `location` _[LocationConfig](#locationconfig)_ | Location configures the location settings during onboarding<br />If not specified, location configuration step is skipped |  | Optional: \{\} <br /> |
| `analytics` _boolean_ | Analytics controls whether to enable analytics during onboarding<br />If not specified, analytics is disabled by default | false | Optional: \{\} <br /> |


#### BootstrapStatus



BootstrapStatus contains the status of the automatic bootstrap process



_Appears in:_
- [HomeAssistantStatus](#homeassistantstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `completed` _boolean_ | Completed indicates whether the bootstrap process has finished successfully |  | Optional: \{\} <br /> |
| `apiTokenReady` _boolean_ | APITokenReady indicates whether the API token has been created and stored |  | Optional: \{\} <br /> |
| `apiTokenSecretName` _string_ | APITokenSecretName is the name of the Secret containing the API token |  | Optional: \{\} <br /> |
| `lastAttempt` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastAttempt is the timestamp of the last bootstrap attempt |  | Optional: \{\} <br /> |
| `message` _string_ | Message provides additional information about the bootstrap status |  | Optional: \{\} <br /> |
| `onboardingDoneFirstSeen` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | OnboardingDoneFirstSeen is the timestamp when /api/onboarding first returned 404.<br />Used to implement confirmation delay without relying on condition LastTransitionTime<br />(which does not update when only the Reason changes). |  | Optional: \{\} <br /> |
| `loginRecoveryAttempts` _integer_ | LoginRecoveryAttempts tracks how many times login recovery was attempted.<br />Reset to zero when onboarding is confirmed fresh or bootstrap succeeds. |  | Optional: \{\} <br /> |


#### ConfigurationReloadStrategy

_Underlying type:_ _string_

ConfigurationReloadStrategy defines how configuration changes should be handled

_Validation:_
- Enum: [auto hot-reload restart]

_Appears in:_
- [HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)

| Field | Description |
| --- | --- |
| `auto` | ConfigurationReloadStrategyAuto - automatically choose best strategy based on config changes<br /> |
| `hot-reload` | ConfigurationReloadStrategyHotReload - attempt to hot-reload via HA REST API<br /> |
| `restart` | ConfigurationReloadStrategyRestart - force full pod restart<br /> |


#### CredentialsSecretRef



CredentialsSecretRef references a Secret containing username and password credentials



_Appears in:_
- [BootstrapCredentials](#bootstrapcredentials)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret |  |  |
| `usernameKey` _string_ | UsernameKey is the key in the Secret containing the username | username | Optional: \{\} <br /> |
| `passwordKey` _string_ | PasswordKey is the key in the Secret containing the password | password | Optional: \{\} <br /> |


#### DevicePassthroughEntry



DevicePassthroughEntry declares one host device node to expose inside the
Home Assistant container.



_Appears in:_
- [AlphaSpec](#alphaspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `hostPath` _string_ | HostPath is the device node's path on the host, e.g. /dev/ttyACM0.<br />Must be an absolute path under /dev. |  | Required: \{\} <br /> |
| `containerPath` _string_ | ContainerPath is the path the device is mounted at inside the Home<br />Assistant container. Defaults to HostPath when omitted. |  | Optional: \{\} <br /> |


#### GatewayParentRef



GatewayParentRef references an existing Gateway listener.



_Appears in:_
- [GatewaySpec](#gatewayspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the existing Gateway. |  |  |
| `namespace` _string_ | Namespace of the Gateway. When different from the HA namespace, the user<br />must provide a ReferenceGrant. |  | Optional: \{\} <br /> |
| `sectionName` _string_ | SectionName is the listener name (e.g. "https"). |  | Optional: \{\} <br /> |


#### GatewaySpec



GatewaySpec configures operator-managed Gateway API exposure for HA. Managing
Gateway API routing resources (sibling to the HA pod) is a stable opt-in — it
does not change the Home Assistant pod's networking or security context, so it
lives at the top level rather than under spec.alpha.



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled turns on operator management of Gateway API routing (HTTPRoute,<br />and optionally a Gateway). | false | Optional: \{\} <br /> |
| `host` _string_ | Host is the hostname for the route and certificate. |  | Optional: \{\} <br /> |
| `issuerRef` _[IssuerReference](#issuerreference)_ | IssuerRef references an existing cert-manager Issuer/ClusterIssuer. When<br />set (and cert-manager available), the operator issues a certificate for<br />the listener. |  | Optional: \{\} <br /> |
| `secretName` _string_ | SecretName references a bring-your-own TLS Secret for the listener.<br />Takes precedence over IssuerRef. |  | Optional: \{\} <br /> |
| `parentRef` _[GatewayParentRef](#gatewayparentref)_ | ParentRef references an existing Gateway/listener to attach the HTTPRoute<br />to. When empty and ManageGateway is true, the operator creates a Gateway. |  | Optional: \{\} <br /> |
| `manageGateway` _boolean_ | ManageGateway controls whether the operator also creates a Gateway<br />resource (not just the HTTPRoute). GatewayClass and the gateway controller<br />remain the platform's responsibility. | false | Optional: \{\} <br /> |
| `filters` _[HTTPRouteFilter](#httproutefilter) array_ | Filters are HTTP route-level behaviors (header modification, redirect, URL<br />rewrite) applied, in order, to the single HTTPRoute rule the operator<br />manages for this instance. Omitted/empty leaves the route unchanged from<br />its default shape. |  | Optional: \{\} <br /> |


#### HTTPConfig



HTTPConfig defines HTTP component configuration



_Appears in:_
- [HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `corsDomains` _string array_ | CorsDomains is a list of allowed CORS origins |  | Optional: \{\} <br /> |
| `trustProxy` _boolean_ | TrustProxy enables trust in X-Forwarded-For header |  | Optional: \{\} <br /> |
| `useXForwardedFor` _boolean_ | UseXForwardedFor enables usage of X-Forwarded-For header |  | Optional: \{\} <br /> |


#### HTTPConfigSource

_Underlying type:_ _string_

HTTPConfigSource is the channel the operator uses for the http: configuration.



_Appears in:_
- [HomeAssistantConfigurationStatus](#homeassistantconfigurationstatus)

| Field | Description |
| --- | --- |
| `Api` | HTTPConfigSourceAPI: delivered through the Home Assistant http config API.<br /> |
| `Yaml` | HTTPConfigSourceYAML: written into configuration.yaml (older Home Assistant).<br /> |


#### HTTPHeader



HTTPHeader is a single HTTP header name/value pair.



_Appears in:_
- [HTTPHeaderFilter](#httpheaderfilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the header. |  | MinLength: 1 <br />Pattern: `^[A-Za-z0-9!#$%&'*+\-.^_\x60\|~]+$` <br />Required: \{\} <br /> |
| `value` _string_ | Value of the header. |  | MinLength: 1 <br />Required: \{\} <br /> |


#### HTTPHeaderFilter



HTTPHeaderFilter adds, sets, or removes HTTP headers. Used for both
RequestHeaderModifier and ResponseHeaderModifier.



_Appears in:_
- [HTTPRouteFilter](#httproutefilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `set` _[HTTPHeader](#httpheader) array_ | Set overwrites headers already present. |  | Optional: \{\} <br /> |
| `add` _[HTTPHeader](#httpheader) array_ | Add appends headers, keeping any existing value. |  | Optional: \{\} <br /> |
| `remove` _string array_ | Remove lists header names to strip. |  | Optional: \{\} <br /> |


#### HTTPPathModifier



HTTPPathModifier describes a path replacement for HTTPRequestRedirectFilter
or HTTPURLRewriteFilter.



_Appears in:_
- [HTTPRequestRedirectFilter](#httprequestredirectfilter)
- [HTTPURLRewriteFilter](#httpurlrewritefilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type selects which of the fields below applies. |  | Enum: [ReplaceFullPath ReplacePrefixMatch] <br />Required: \{\} <br /> |
| `replaceFullPath` _string_ | ReplaceFullPath is the whole replacement path. Must be set, and only be<br />set, when Type is ReplaceFullPath. |  | Optional: \{\} <br /> |
| `replacePrefixMatch` _string_ | ReplacePrefixMatch is the replacement for the matched path prefix. Must<br />be set, and only be set, when Type is ReplacePrefixMatch. |  | Optional: \{\} <br /> |


#### HTTPRequestRedirectFilter



HTTPRequestRedirectFilter redirects the request to a different
scheme/hostname/path/port, optionally with a specific status code.



_Appears in:_
- [HTTPRouteFilter](#httproutefilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `scheme` _string_ | Scheme replaces the request scheme (e.g. "https"). |  | Optional: \{\} <br /> |
| `hostname` _string_ | Hostname replaces the request hostname. |  | Optional: \{\} <br /> |
| `path` _[HTTPPathModifier](#httppathmodifier)_ | Path replaces the request path. |  | Optional: \{\} <br /> |
| `port` _integer_ | Port replaces the request port. |  | Optional: \{\} <br /> |
| `statusCode` _integer_ | StatusCode is the redirect status code. |  | Enum: [301 302 303 307 308] <br />Optional: \{\} <br /> |


#### HTTPRouteFilter



HTTPRouteFilter is one user-declared route behavior attached to the HTTP
route exposing a HomeAssistant instance through Gateway API. Mirrors the
field names/shape of upstream Gateway API's own HTTPRouteFilter, limited to
the four supported types: RequestHeaderModifier, ResponseHeaderModifier,
RequestRedirect, URLRewrite. Exactly the sub-object matching Type must be
set; the webhook rejects any other combination.



_Appears in:_
- [GatewaySpec](#gatewayspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _string_ | Type selects which of the sub-objects below applies. |  | Enum: [RequestHeaderModifier ResponseHeaderModifier RequestRedirect URLRewrite] <br />Required: \{\} <br /> |
| `requestHeaderModifier` _[HTTPHeaderFilter](#httpheaderfilter)_ | RequestHeaderModifier modifies request headers. Must be set, and only be<br />set, when Type is RequestHeaderModifier. |  | Optional: \{\} <br /> |
| `responseHeaderModifier` _[HTTPHeaderFilter](#httpheaderfilter)_ | ResponseHeaderModifier modifies response headers. Must be set, and only<br />be set, when Type is ResponseHeaderModifier. |  | Optional: \{\} <br /> |
| `requestRedirect` _[HTTPRequestRedirectFilter](#httprequestredirectfilter)_ | RequestRedirect redirects the request. Must be set, and only be set,<br />when Type is RequestRedirect. |  | Optional: \{\} <br /> |
| `urlRewrite` _[HTTPURLRewriteFilter](#httpurlrewritefilter)_ | URLRewrite rewrites the request path/hostname. Must be set, and only be<br />set, when Type is URLRewrite. |  | Optional: \{\} <br /> |


#### HTTPURLRewriteFilter



HTTPURLRewriteFilter rewrites the request hostname/path before it reaches
Home Assistant.



_Appears in:_
- [HTTPRouteFilter](#httproutefilter)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `hostname` _string_ | Hostname replaces the request hostname. |  | Optional: \{\} <br /> |
| `path` _[HTTPPathModifier](#httppathmodifier)_ | Path replaces the request path. |  | Optional: \{\} <br /> |


#### HomeAssistant



HomeAssistant is the Schema for the homeassistants API.



_Appears in:_
- [HomeAssistantList](#homeassistantlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistant` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantSpec](#homeassistantspec)_ |  |  |  |
| `status` _[HomeAssistantStatus](#homeassistantstatus)_ |  |  |  |


#### HomeAssistantArea



HomeAssistantArea is the Schema for the homeassistantareas API



_Appears in:_
- [HomeAssistantAreaList](#homeassistantarealist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantArea` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[HomeAssistantAreaSpec](#homeassistantareaspec)_ |  |  | Required: \{\} <br /> |
| `status` _[HomeAssistantAreaStatus](#homeassistantareastatus)_ |  |  | Optional: \{\} <br /> |


#### HomeAssistantAreaList



HomeAssistantAreaList contains a list of HomeAssistantArea





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantAreaList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantArea](#homeassistantarea) array_ |  |  |  |


#### HomeAssistantAreaSpec



HomeAssistantAreaSpec defines the desired state of HomeAssistantArea



_Appears in:_
- [HomeAssistantArea](#homeassistantarea)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | homeAssistantRef is a reference to the HomeAssistant CR this area belongs to |  | Required: \{\} <br /> |
| `name` _string_ | name is the display name of the area in Home Assistant |  | MinLength: 1 <br />Required: \{\} <br /> |
| `floorName` _string_ | floorName is the name of the HomeAssistantFloor CR to assign this area to (resolved at reconcile time) |  | Optional: \{\} <br /> |
| `icon` _string_ | icon is the Material Design Icon for the area (e.g. "mdi:sofa") |  | Optional: \{\} <br /> |
| `labels` _string array_ | labels is a list of HomeAssistantLabel CR names to assign to this area (resolved at reconcile time) |  | Optional: \{\} <br /> |


#### HomeAssistantAreaStatus



HomeAssistantAreaStatus defines the observed state of HomeAssistantArea



_Appears in:_
- [HomeAssistantArea](#homeassistantarea)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `areaID` _string_ | areaID is the ID assigned by Home Assistant after creation |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | observedGeneration is the most recent generation observed |  | Optional: \{\} <br /> |
| `lastError` _string_ | lastError contains the error message from the last failed operation<br />Cleared when operation succeeds |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | conditions represent the current state of the HomeAssistantArea resource |  | Optional: \{\} <br /> |


#### HomeAssistantAutomation



HomeAssistantAutomation is the Schema for the homeassistantautomations API



_Appears in:_
- [HomeAssistantAutomationList](#homeassistantautomationlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantAutomation` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantAutomationSpec](#homeassistantautomationspec)_ |  |  |  |
| `status` _[HomeAssistantAutomationStatus](#homeassistantautomationstatus)_ |  |  |  |


#### HomeAssistantAutomationList



HomeAssistantAutomationList contains a list of HomeAssistantAutomation





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantAutomationList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantAutomation](#homeassistantautomation) array_ |  |  |  |


#### HomeAssistantAutomationSpec



HomeAssistantAutomationSpec defines the desired state of HomeAssistantAutomation



_Appears in:_
- [HomeAssistantAutomation](#homeassistantautomation)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | HomeAssistantRef references the HomeAssistant CR that will use this automation |  | Required: \{\} <br /> |
| `id` _string_ | ID is a unique identifier for the automation (used by Home Assistant)<br />If not specified, will be auto-generated from the CR name<br />Must contain only lowercase letters, digits, and underscores. Existing<br />resources with an id predating this constraint (uppercase letters or<br />hyphens) keep working until their next update, which will then be<br />rejected until id is renamed to a conforming value. |  | Pattern: `^[a-z][a-z0-9_]*$` <br />Optional: \{\} <br /> |
| `alias` _string_ | Alias is a user-friendly name for the automation |  | MinLength: 1 <br />Required: \{\} <br /> |
| `description` _string_ | Description provides details about what the automation does |  | Optional: \{\} <br /> |
| `triggers` _[AutomationTrigger](#automationtrigger) array_ | Triggers define the events that will trigger this automation<br />At least one trigger is required |  | MinItems: 1 <br />Required: \{\} <br /> |
| `conditions` _[AutomationCondition](#automationcondition) array_ | Conditions define requirements that must be met for the automation to execute<br />All conditions must evaluate to true for the automation to run |  | Optional: \{\} <br /> |
| `actions` _[AutomationAction](#automationaction) array_ | Actions define the sequence of tasks to execute when triggered<br />At least one action is required |  | MinItems: 1 <br />Required: \{\} <br /> |
| `mode` _[AutomationMode](#automationmode)_ | Mode defines how the automation should behave when triggered again while already running | single | Enum: [single restart queued parallel] <br />Optional: \{\} <br /> |
| `max` _integer_ | Max defines the maximum number of concurrent or queued runs (for queued/parallel modes) | 10 | Minimum: 1 <br />Optional: \{\} <br /> |
| `maxExceeded` _string_ | MaxExceeded defines the log severity level when max is exceeded (silent, info, warning, error) | warning | Enum: [silent info warning error] <br />Optional: \{\} <br /> |
| `initialState` _boolean_ | InitialState defines whether the automation should be enabled at startup<br />If not specified, the last state is restored |  | Optional: \{\} <br /> |
| `autoReload` _boolean_ | AutoReload enables automatic hot-reload when automation changes<br />If false, requires manual reload or pod restart | true | Optional: \{\} <br /> |
| `enabled` _boolean_ | Enabled controls whether this automation is active<br />Can be used to temporarily disable without deleting the CR | true | Optional: \{\} <br /> |


#### HomeAssistantAutomationStatus



HomeAssistantAutomationStatus defines the observed state of HomeAssistantAutomation



_Appears in:_
- [HomeAssistantAutomation](#homeassistantautomation)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the latest available observations of the HomeAssistantAutomation state |  | Optional: \{\} <br /> |
| `automationHash` _string_ | AutomationHash is the SHA256 hash of the current automation configuration<br />Used to detect changes and determine if reload is needed |  | Optional: \{\} <br /> |
| `lastReloadTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastReloadTime is the timestamp of the last successful reload |  | Optional: \{\} <br /> |
| `lastTriggeredTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastTriggeredTime is the timestamp when the automation was last triggered (if available from HA) |  | Optional: \{\} <br /> |
| `lastError` _string_ | LastError contains the error message from the last failed operation<br />Cleared when operation succeeds |  | Optional: \{\} <br /> |
| `lastReloadMethod` _string_ | LastReloadMethod indicates how the last reload was performed<br />Possible values: "hot-reload", "restart", "none" |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed HomeAssistantAutomation |  | Optional: \{\} <br /> |


#### HomeAssistantConfiguration



HomeAssistantConfiguration is the Schema for the homeassistantconfigurations API.



_Appears in:_
- [HomeAssistantConfigurationList](#homeassistantconfigurationlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantConfiguration` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)_ |  |  |  |
| `status` _[HomeAssistantConfigurationStatus](#homeassistantconfigurationstatus)_ |  |  |  |


#### HomeAssistantConfigurationList



HomeAssistantConfigurationList contains a list of HomeAssistantConfiguration.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantConfigurationList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantConfiguration](#homeassistantconfiguration) array_ |  |  |  |


#### HomeAssistantConfigurationSpec



HomeAssistantConfigurationSpec defines the desired state of HomeAssistantConfiguration



_Appears in:_
- [HomeAssistantConfiguration](#homeassistantconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | HomeAssistantRef references the HomeAssistant CR that will use this configuration |  | Required: \{\} <br /> |
| `configuration` _string_ | Configuration contains the full configuration.yaml content as a string<br />This is the raw YAML configuration for Home Assistant |  | Required: \{\} <br /> |
| `reloadStrategy` _[ConfigurationReloadStrategy](#configurationreloadstrategy)_ | ReloadStrategy defines how configuration changes should be applied | auto | Enum: [auto hot-reload restart] <br />Optional: \{\} <br /> |
| `autoReload` _boolean_ | AutoReload enables automatic reloading/restart when configuration changes | true | Optional: \{\} <br /> |
| `http` _[HTTPConfig](#httpconfig)_ | HTTP component configuration (optional typed section) |  | Optional: \{\} <br /> |
| `logger` _[LoggerConfig](#loggerconfig)_ | Logger component configuration (optional typed section) |  | Optional: \{\} <br /> |
| `recorder` _[RecorderConfig](#recorderconfig)_ | Recorder component configuration (optional typed section) |  | Optional: \{\} <br /> |
| `mqtt` _[MQTTConfig](#mqttconfig)_ | MQTT component configuration (optional typed section) |  | Optional: \{\} <br /> |


#### HomeAssistantConfigurationStatus



HomeAssistantConfigurationStatus defines the observed state of HomeAssistantConfiguration



_Appears in:_
- [HomeAssistantConfiguration](#homeassistantconfiguration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the latest available observations of the HomeAssistantConfiguration state |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is the SHA256 hash of the current configuration<br />Used to detect changes and determine if reload is needed |  | Optional: \{\} <br /> |
| `lastReloadTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastReloadTime is the timestamp of the last successful reload/restart |  | Optional: \{\} <br /> |
| `lastReloadMethod` _string_ | LastReloadMethod indicates how the last reload was performed (hot-reload or restart) |  | Optional: \{\} <br /> |
| `lastError` _string_ | LastError contains the error message from the last failed reload attempt<br />Cleared when reload succeeds |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | Generation tracks the generation of the spec that the status reflects |  | Optional: \{\} <br /> |
| `trustedProxiesDefaulted` _boolean_ | TrustedProxiesDefaulted reports whether the operator's default<br />http.trusted_proxies / http.use_x_forwarded_for values are currently<br />active in the generated configuration for the referenced HomeAssistant.<br />false covers every case where they are not active (not exposed via<br />Ingress/Gateway, opted out via spec.disableDefaultTrustedProxies, or the<br />user already manages these keys themselves) — see the HomeAssistant's own<br />ExposureReady condition message for which of those it is. |  | Optional: \{\} <br /> |
| `httpConfigSource` _[HTTPConfigSource](#httpconfigsource)_ | HTTPConfigSource reports which channel the operator uses to deliver the<br />http: configuration to the referenced HomeAssistant: "Api" on Home<br />Assistant 2026.8+ (the http config WebSocket API), "Yaml" on older<br />versions (the http: block in configuration.yaml). Empty until the operator<br />has been able to determine it (the instance not yet reachable). |  | Enum: [Api Yaml] <br />Optional: \{\} <br /> |


#### HomeAssistantFloor



HomeAssistantFloor is the Schema for the homeassistantfloors API



_Appears in:_
- [HomeAssistantFloorList](#homeassistantfloorlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantFloor` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[HomeAssistantFloorSpec](#homeassistantfloorspec)_ |  |  | Required: \{\} <br /> |
| `status` _[HomeAssistantFloorStatus](#homeassistantfloorstatus)_ |  |  | Optional: \{\} <br /> |


#### HomeAssistantFloorList



HomeAssistantFloorList contains a list of HomeAssistantFloor





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantFloorList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantFloor](#homeassistantfloor) array_ |  |  |  |


#### HomeAssistantFloorSpec



HomeAssistantFloorSpec defines the desired state of HomeAssistantFloor



_Appears in:_
- [HomeAssistantFloor](#homeassistantfloor)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | homeAssistantRef is a reference to the HomeAssistant CR this floor belongs to |  | Required: \{\} <br /> |
| `name` _string_ | name is the display name of the floor in Home Assistant |  | MinLength: 1 <br />Required: \{\} <br /> |
| `level` _integer_ | level is the floor level (e.g. 0 for ground, 1 for first, -1 for basement) |  | Optional: \{\} <br /> |
| `icon` _string_ | icon is the Material Design Icon for the floor (e.g. "mdi:home-floor-1") |  | Optional: \{\} <br /> |


#### HomeAssistantFloorStatus



HomeAssistantFloorStatus defines the observed state of HomeAssistantFloor



_Appears in:_
- [HomeAssistantFloor](#homeassistantfloor)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `floorID` _string_ | floorID is the ID assigned by Home Assistant after creation |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | observedGeneration is the most recent generation observed |  | Optional: \{\} <br /> |
| `lastError` _string_ | lastError contains the error message from the last failed operation<br />Cleared when operation succeeds |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | conditions represent the current state of the HomeAssistantFloor resource |  | Optional: \{\} <br /> |


#### HomeAssistantIntegration



HomeAssistantIntegration manages a Home Assistant integration (config entry) via the Config Flow API.
It supports create, adopt, reconfigure (delete+re-create on spec change), and cleanup via finalizer.
Only single-step Config Flows are supported (e.g. mqtt, recorder, esphome).



_Appears in:_
- [HomeAssistantIntegrationList](#homeassistantintegrationlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantIntegration` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantIntegrationSpec](#homeassistantintegrationspec)_ |  |  |  |
| `status` _[HomeAssistantIntegrationStatus](#homeassistantintegrationstatus)_ |  |  |  |


#### HomeAssistantIntegrationList



HomeAssistantIntegrationList contains a list of HomeAssistantIntegration





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantIntegrationList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantIntegration](#homeassistantintegration) array_ |  |  |  |


#### HomeAssistantIntegrationSpec



HomeAssistantIntegrationSpec defines the desired state of HomeAssistantIntegration



_Appears in:_
- [HomeAssistantIntegration](#homeassistantintegration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | HomeAssistantRef references the HomeAssistant instance to configure |  | Required: \{\} <br /> |
| `domain` _string_ | Domain is the integration name in Home Assistant (e.g. "mqtt", "esphome", "recorder") |  | MinLength: 1 <br />Required: \{\} <br /> |
| `configuration` _object (keys:string, values:[IntegrationValue](#integrationvalue))_ | Configuration contains fields submitted to the Config Flow (single-step flows only).<br />Keys are field names from the data_schema; values are plain text or Secret references. |  | Optional: \{\} <br /> |


#### HomeAssistantIntegrationStatus



HomeAssistantIntegrationStatus defines the observed state of HomeAssistantIntegration



_Appears in:_
- [HomeAssistantIntegration](#homeassistantintegration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `entryID` _string_ | EntryID is the Home Assistant config entry ID created or adopted by the Config Flow |  | Optional: \{\} <br /> |
| `configHash` _string_ | ConfigHash is the SHA256 hash of the resolved configuration values.<br />Used to detect spec changes and trigger reconfiguration (delete + re-create). |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the latest available observations of the integration state |  | Optional: \{\} <br /> |
| `lastError` _string_ | LastError contains the error message from the last failed operation<br />Cleared when operation succeeds |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed CR |  | Optional: \{\} <br /> |


#### HomeAssistantLabel



HomeAssistantLabel is the Schema for the homeassistantlabels API



_Appears in:_
- [HomeAssistantLabelList](#homeassistantlabellist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantLabel` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[HomeAssistantLabelSpec](#homeassistantlabelspec)_ |  |  | Required: \{\} <br /> |
| `status` _[HomeAssistantLabelStatus](#homeassistantlabelstatus)_ |  |  | Optional: \{\} <br /> |


#### HomeAssistantLabelList



HomeAssistantLabelList contains a list of HomeAssistantLabel





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantLabelList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantLabel](#homeassistantlabel) array_ |  |  |  |


#### HomeAssistantLabelSpec



HomeAssistantLabelSpec defines the desired state of HomeAssistantLabel



_Appears in:_
- [HomeAssistantLabel](#homeassistantlabel)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | homeAssistantRef is a reference to the HomeAssistant CR this label belongs to |  | Required: \{\} <br /> |
| `name` _string_ | name is the display name of the label in Home Assistant |  | MinLength: 1 <br />Required: \{\} <br /> |
| `icon` _string_ | icon is the Material Design Icon for the label (e.g. "mdi:tag") |  | Optional: \{\} <br /> |
| `color` _string_ | color is the label color (e.g. "red", "blue", "green") |  | Optional: \{\} <br /> |


#### HomeAssistantLabelStatus



HomeAssistantLabelStatus defines the observed state of HomeAssistantLabel



_Appears in:_
- [HomeAssistantLabel](#homeassistantlabel)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `labelID` _string_ | labelID is the ID assigned by Home Assistant after creation |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | observedGeneration is the most recent generation observed |  | Optional: \{\} <br /> |
| `lastError` _string_ | lastError contains the error message from the last failed operation<br />Cleared when operation succeeds |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | conditions represent the current state of the HomeAssistantLabel resource |  | Optional: \{\} <br /> |


#### HomeAssistantList



HomeAssistantList contains a list of HomeAssistant.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistant](#homeassistant) array_ |  |  |  |


#### HomeAssistantPhase

_Underlying type:_ _string_

HomeAssistantPhase represents the current phase of the HomeAssistant instance.

_Validation:_
- Enum: [Pending Running Failed Unknown]

_Appears in:_
- [HomeAssistantStatus](#homeassistantstatus)

| Field | Description |
| --- | --- |
| `Pending` |  |
| `Running` |  |
| `Failed` |  |
| `Unknown` |  |


#### HomeAssistantReference



HomeAssistantReference references a HomeAssistant CR.



_Appears in:_
- [HomeAssistantAreaSpec](#homeassistantareaspec)
- [HomeAssistantAutomationSpec](#homeassistantautomationspec)
- [HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)
- [HomeAssistantFloorSpec](#homeassistantfloorspec)
- [HomeAssistantIntegrationSpec](#homeassistantintegrationspec)
- [HomeAssistantLabelSpec](#homeassistantlabelspec)
- [HomeAssistantSceneSpec](#homeassistantscenespec)
- [HomeAssistantScriptSpec](#homeassistantscriptspec)
- [HomeAssistantSecretsSpec](#homeassistantsecretsspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the HomeAssistant resource |  | MinLength: 1 <br />Required: \{\} <br /> |


#### HomeAssistantScene



HomeAssistantScene is the Schema for the homeassistantscenes API



_Appears in:_
- [HomeAssistantSceneList](#homeassistantscenelist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantScene` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantSceneSpec](#homeassistantscenespec)_ |  |  |  |
| `status` _[HomeAssistantSceneStatus](#homeassistantscenestatus)_ |  |  |  |


#### HomeAssistantSceneList



HomeAssistantSceneList contains a list of HomeAssistantScene





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantSceneList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantScene](#homeassistantscene) array_ |  |  |  |


#### HomeAssistantSceneSpec



HomeAssistantSceneSpec defines the desired state of HomeAssistantScene



_Appears in:_
- [HomeAssistantScene](#homeassistantscene)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | HomeAssistantRef references the HomeAssistant CR that will use this scene |  | Required: \{\} <br /> |
| `id` _string_ | ID is a unique identifier for the scene (used by Home Assistant)<br />If not specified, will be auto-generated from the CR name<br />Must contain only lowercase letters, digits, and underscores. Existing<br />resources with an id predating this constraint (uppercase letters or<br />hyphens) keep working until their next update, which will then be<br />rejected until id is renamed to a conforming value. |  | Pattern: `^[a-z][a-z0-9_]*$` <br />Optional: \{\} <br /> |
| `name` _string_ | Name is a user-friendly name for the scene (displayed in Home Assistant UI)<br />If not specified, the CR name will be used |  | MinLength: 1 <br />Optional: \{\} <br /> |
| `icon` _string_ | Icon is a Material Design icon for the scene (e.g., "mdi:movie", "mdi:candle")<br />See https://mdi.bessarabov.com/ for available icons |  | Optional: \{\} <br /> |
| `entities` _[SceneEntity](#sceneentity) array_ | Entities define the list of devices and their states to set when scene is activated<br />At least one entity is required |  | MinItems: 1 <br />Required: \{\} <br /> |
| `autoReload` _boolean_ | AutoReload enables automatic hot-reload when scene configuration changes<br />If false, requires manual reload or pod restart | true | Optional: \{\} <br /> |


#### HomeAssistantSceneStatus



HomeAssistantSceneStatus defines the observed state of HomeAssistantScene



_Appears in:_
- [HomeAssistantScene](#homeassistantscene)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the latest available observations of the HomeAssistantScene state |  | Optional: \{\} <br /> |
| `sceneHash` _string_ | SceneHash is the SHA256 hash of the current scene configuration<br />Used to detect changes and determine if reload is needed |  | Optional: \{\} <br /> |
| `lastReloadTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastReloadTime is the timestamp of the last successful reload |  | Optional: \{\} <br /> |
| `lastActivated` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastActivated is the timestamp when the scene was last activated (if available from HA) |  | Optional: \{\} <br /> |
| `lastError` _string_ | LastError contains the error message from the last failed operation<br />Cleared when operation succeeds |  | Optional: \{\} <br /> |
| `entityCount` _integer_ | EntityCount is the number of entities defined in the scene<br />Updated by the controller for display in kubectl output |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed HomeAssistantScene |  | Optional: \{\} <br /> |


#### HomeAssistantScript



HomeAssistantScript is the Schema for the homeassistantscripts API



_Appears in:_
- [HomeAssistantScriptList](#homeassistantscriptlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantScript` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantScriptSpec](#homeassistantscriptspec)_ |  |  |  |
| `status` _[HomeAssistantScriptStatus](#homeassistantscriptstatus)_ |  |  |  |


#### HomeAssistantScriptList



HomeAssistantScriptList contains a list of HomeAssistantScript





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantScriptList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantScript](#homeassistantscript) array_ |  |  |  |


#### HomeAssistantScriptSpec



HomeAssistantScriptSpec defines the desired state of HomeAssistantScript



_Appears in:_
- [HomeAssistantScript](#homeassistantscript)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | HomeAssistantRef references the HomeAssistant CR that will use this script |  | Required: \{\} <br /> |
| `id` _string_ | ID is a unique identifier for the script (used by Home Assistant)<br />If not specified, will be auto-generated from the CR name<br />Must contain only lowercase letters, digits, and underscores |  | Pattern: `^[a-z][a-z0-9_]*$` <br />Optional: \{\} <br /> |
| `alias` _string_ | Alias is a user-friendly name for the script |  | MinLength: 1 <br />Required: \{\} <br /> |
| `description` _string_ | Description provides details about what the script does |  | Optional: \{\} <br /> |
| `icon` _string_ | Icon is a Material Design icon for the script (e.g., "mdi:script", "mdi:play")<br />See https://mdi.bessarabov.com/ for available icons |  | Optional: \{\} <br /> |
| `sequence` _[ScriptAction](#scriptaction) array_ | Sequence defines the list of actions to execute when the script is called<br />At least one action is required |  | MinItems: 1 <br />Required: \{\} <br /> |
| `fields` _object (keys:string, values:[ScriptField](#scriptfield))_ | Fields define input parameters for the script<br />These allow the script to accept arguments when called |  | Optional: \{\} <br /> |
| `mode` _[ScriptMode](#scriptmode)_ | Mode defines how the script should behave when called again while already running | single | Enum: [single restart queued parallel] <br />Optional: \{\} <br /> |
| `max` _integer_ | Max defines the maximum number of concurrent or queued runs (for queued/parallel modes) | 10 | Minimum: 1 <br />Optional: \{\} <br /> |
| `maxExceeded` _string_ | MaxExceeded defines the log severity level when max is exceeded (silent, info, warning, error) | warning | Enum: [silent info warning error] <br />Optional: \{\} <br /> |
| `autoReload` _boolean_ | AutoReload enables automatic hot-reload when script changes<br />If false, requires manual reload or pod restart | true | Optional: \{\} <br /> |


#### HomeAssistantScriptStatus



HomeAssistantScriptStatus defines the observed state of HomeAssistantScript



_Appears in:_
- [HomeAssistantScript](#homeassistantscript)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the latest available observations of the HomeAssistantScript state |  | Optional: \{\} <br /> |
| `scriptHash` _string_ | ScriptHash is the SHA256 hash of the current script configuration<br />Used to detect changes and determine if reload is needed |  | Optional: \{\} <br /> |
| `lastReloadTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastReloadTime is the timestamp of the last successful reload |  | Optional: \{\} <br /> |
| `lastRunTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastRunTime is the timestamp when the script was last executed (if available from HA) |  | Optional: \{\} <br /> |
| `lastError` _string_ | LastError contains the error message from the last failed operation<br />Cleared when operation succeeds |  | Optional: \{\} <br /> |
| `lastReloadMethod` _string_ | LastReloadMethod indicates how the last reload was performed<br />Possible values: "hot-reload" (success), "failed" (all retries exhausted), "none" (skipped/initial) |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed HomeAssistantScript |  | Optional: \{\} <br /> |


#### HomeAssistantSecrets



HomeAssistantSecrets is the Schema for the homeassistantsecrets API.



_Appears in:_
- [HomeAssistantSecretsList](#homeassistantsecretslist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantSecrets` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantSecretsSpec](#homeassistantsecretsspec)_ |  |  |  |
| `status` _[HomeAssistantSecretsStatus](#homeassistantsecretsstatus)_ |  |  |  |


#### HomeAssistantSecretsList



HomeAssistantSecretsList contains a list of HomeAssistantSecrets.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1` | | |
| `kind` _string_ | `HomeAssistantSecretsList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantSecrets](#homeassistantsecrets) array_ |  |  |  |


#### HomeAssistantSecretsSpec



HomeAssistantSecretsSpec defines the desired state of HomeAssistantSecrets.



_Appears in:_
- [HomeAssistantSecrets](#homeassistantsecrets)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | HomeAssistantRef references the HomeAssistant CR that will use these secrets |  | Required: \{\} <br /> |
| `secretRefs` _[SecretKeyReference](#secretkeyreference) array_ | SecretRefs is a list of references to Kubernetes Secrets.<br />Keys from these Secrets will be merged into the generated secrets.yaml |  | MinItems: 1 <br />Required: \{\} <br /> |
| `autoRestart` _boolean_ | AutoRestart controls whether the Home Assistant pod should be automatically<br />restarted when secrets change. When enabled, the controller updates an annotation<br />on the StatefulSet to trigger a rolling restart. | true | Optional: \{\} <br /> |


#### HomeAssistantSecretsStatus



HomeAssistantSecretsStatus defines the observed state of HomeAssistantSecrets.



_Appears in:_
- [HomeAssistantSecrets](#homeassistantsecrets)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the latest available observations of the HomeAssistantSecrets state |  | Optional: \{\} <br /> |
| `secretsHash` _string_ | SecretsHash is the SHA256 hash of the generated secrets.yaml content.<br />Used to detect changes and trigger pod restarts. |  | Optional: \{\} <br /> |
| `lastUpdated` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastUpdated is the timestamp when the secrets were last updated |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed HomeAssistantSecrets |  | Optional: \{\} <br /> |
| `lastError` _string_ | LastError contains a human-readable description of the last error encountered |  | Optional: \{\} <br /> |


#### HomeAssistantSpec



HomeAssistantSpec defines the desired state of HomeAssistant.



_Appears in:_
- [HomeAssistant](#homeassistant)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version is the Home Assistant version/tag to deploy (e.g., "2024.1.0", "stable", "latest") | stable | Optional: \{\} <br /> |
| `image` _string_ | Image allows overriding the default Home Assistant image | ghcr.io/home-assistant/home-assistant | Optional: \{\} <br /> |
| `storage` _[StorageSpec](#storagespec)_ | Storage configuration for Home Assistant data |  | Optional: \{\} <br /> |
| `additionalVolumes` _[AdditionalVolumesSpec](#additionalvolumesspec)_ | Additional volumes and mounts for the Home Assistant pod |  | Optional: \{\} <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#resourcerequirements-v1-core)_ | Resources defines CPU and memory requests/limits |  | Optional: \{\} <br /> |
| `service` _[ServiceSpec](#servicespec)_ | Service configuration for exposing Home Assistant |  | Optional: \{\} <br /> |
| `ingress` _[IngressSpec](#ingressspec)_ | Ingress configuration for external access |  | Optional: \{\} <br /> |
| `gateway` _[GatewaySpec](#gatewayspec)_ | Gateway configures operator-managed Gateway API exposure (HTTPRoute, and<br />optionally a Gateway) for Home Assistant, with optional cert-manager TLS. |  | Optional: \{\} <br /> |
| `timezone` _string_ | Timezone for the Home Assistant instance (e.g., "Europe/Warsaw") | UTC | Optional: \{\} <br /> |
| `secretsFrom` _[SecretReference](#secretreference)_ | SecretsFrom references a Secret containing secrets.yaml<br />The Secret should have a key "secrets.yaml" with the HA secrets |  | Optional: \{\} <br /> |
| `hostNetwork` _boolean_ | HostNetwork enables host networking for the Home Assistant pod.<br />When true, the pod uses the host's network namespace, enabling discovery<br />of IoT devices via mDNS, SSDP, and DHCP on the local network. |  | Optional: \{\} <br /> |
| `bootstrap` _[BootstrapSpec](#bootstrapspec)_ | Bootstrap configures automatic onboarding and API token creation<br />When enabled, the operator will automatically complete the Home Assistant<br />onboarding process and create a long-lived access token for API access |  | Optional: \{\} <br /> |
| `backup` _[BackupSpec](#backupspec)_ | Backup configures automatic backups using Home Assistant's built-in backup system.<br />Requires bootstrap with API token enabled. |  | Optional: \{\} <br /> |
| `disableDefaultTrustedProxies` _boolean_ | DisableDefaultTrustedProxies opts out of the operator's automatic<br />http.trusted_proxies / http.use_x_forwarded_for defaults. When Ingress or<br />Gateway API exposure is enabled, Home Assistant rejects every request<br />through that endpoint with 400 Bad Request until it trusts the proxy<br />forwarding the request. Unless this is set to true, and unless the user<br />has already set these keys themselves in HomeAssistantConfiguration (or<br />manages http: entirely externally, e.g. via an !include tag), the<br />operator injects the RFC1918 private address ranges (10.0.0.0/8,<br />172.16.0.0/12, 192.168.0.0/16) as sensible defaults — this cannot be a<br />reliable autodetection of the real cluster CIDR, only a conservative<br />guess, so this field exists to opt out entirely for clusters where it<br />doesn't apply (e.g. non-RFC1918 pod/service networks, or where other<br />workloads on the pod network should not be trusted to set<br />X-Forwarded-For for the actual Ingress/Gateway proxy). |  | Optional: \{\} <br /> |
| `scheduling` _[SchedulingSpec](#schedulingspec)_ | Scheduling controls where the Home Assistant pod is eligible to run and<br />how it is treated under resource contention, using Kubernetes' own<br />well-tested scheduling primitives directly (node selector, node/pod<br />affinity and anti-affinity, tolerations, priority class) rather than a<br />project-specific abstraction. Ships on the stable spec (not<br />spec.alpha.*): the operator only passes these fields through to the<br />generated pod template unchanged, it does not implement any new<br />scheduling behavior of its own. |  | Optional: \{\} <br /> |
| `alpha` _[AlphaSpec](#alphaspec)_ | Alpha groups experimental, unstable fields. Fields here may change or be<br />removed without a deprecation notice. |  | Optional: \{\} <br /> |


#### HomeAssistantStatus



HomeAssistantStatus defines the observed state of HomeAssistant.



_Appears in:_
- [HomeAssistant](#homeassistant)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[HomeAssistantPhase](#homeassistantphase)_ | Phase represents the current lifecycle phase of the HomeAssistant instance |  | Enum: [Pending Running Failed Unknown] <br />Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the latest available observations of the HomeAssistant state |  | Optional: \{\} <br /> |
| `version` _string_ | Version is the currently deployed Home Assistant version |  | Optional: \{\} <br /> |
| `url` _string_ | URL is the access URL for Home Assistant (if Ingress is enabled) |  | Optional: \{\} <br /> |
| `ready` _boolean_ | Ready indicates if the Home Assistant instance is ready to serve traffic |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed HomeAssistant |  | Optional: \{\} <br /> |
| `bootstrap` _[BootstrapStatus](#bootstrapstatus)_ | BootstrapStatus contains the status of the automatic bootstrap process |  | Optional: \{\} <br /> |
| `selfUnbanCount` _integer_ | SelfUnbanCount is the total number of ban-recovery pod restarts. Kept for<br />backwards compatibility; prefer BanRestartWindowCount for limit enforcement. |  | Optional: \{\} <br /> |
| `lastSelfUnban` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastSelfUnban is the timestamp of the most recent ban-recovery pod restart. |  | Optional: \{\} <br /> |
| `banRestartWindowStart` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | BanRestartWindowStart is the start of the current ban-recovery sliding window.<br />Nil means no window is active (no ban seen or window has expired). |  | Optional: \{\} <br /> |
| `banRestartWindowCount` _integer_ | BanRestartWindowCount is the number of ban-recovery pod restarts within the<br />current sliding window. When it reaches banRestartMaxCount the operator stops<br />restarting and sets condition BanRecoveryFailed=True. |  | Optional: \{\} <br /> |


#### IngressSpec



IngressSpec defines external access configuration.



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether an Ingress resource is created | false | Optional: \{\} <br /> |
| `host` _string_ | Host is the hostname for the Ingress (e.g., "ha.example.com") |  | Optional: \{\} <br /> |
| `ingressClassName` _string_ | IngressClassName specifies the Ingress controller to use |  | Optional: \{\} <br /> |
| `tls` _[IngressTLSSpec](#ingresstlsspec)_ | TLS configuration |  | Optional: \{\} <br /> |
| `annotations` _object (keys:string, values:string)_ | Annotations to add to the Ingress resource |  | Optional: \{\} <br /> |


#### IngressTLSSpec



IngressTLSSpec defines TLS configuration for Ingress.



_Appears in:_
- [IngressSpec](#ingressspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether TLS is enabled | false | Optional: \{\} <br /> |
| `secretName` _string_ | SecretName containing the TLS certificate. When set, it is used as-is<br />(bring-your-own) and takes precedence over IssuerRef. |  | Optional: \{\} <br /> |
| `issuerRef` _[IssuerReference](#issuerreference)_ | IssuerRef references an existing cert-manager Issuer/ClusterIssuer. When<br />set and cert-manager is available, the operator creates a Certificate for<br />the Ingress TLS Secret. Ignored when SecretName is provided. |  | Optional: \{\} <br /> |


#### InitContainerSpec



InitContainerSpec configures the image used for the config-init init container.



_Appears in:_
- [StorageSpec](#storagespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `repository` _string_ | Repository is the container image repository (e.g. "docker.io/library") | docker.io/library | Optional: \{\} <br /> |
| `image` _string_ | Image is the container image name (e.g. "busybox") | busybox | Optional: \{\} <br /> |
| `tag` _string_ | Tag is the container image tag (e.g. "1.36", "latest") | 1.36 | Optional: \{\} <br /> |


#### IntegrationSecretKeyRef



IntegrationSecretKeyRef references a specific key within a Kubernetes Secret



_Appears in:_
- [IntegrationValue](#integrationvalue)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret |  | MinLength: 1 <br /> |
| `key` _string_ | Key within the Secret |  | MinLength: 1 <br /> |


#### IntegrationValue



IntegrationValue holds a plain text value, a JSON value, or a reference to a Kubernetes Secret key.
Exactly one of Value, JSONValue, or SecretKeyRef must be set.



_Appears in:_
- [HomeAssistantIntegrationSpec](#homeassistantintegrationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `value` _string_ | Value is a plain text configuration value sent as a string to the Config Flow API. |  | Optional: \{\} <br /> |
| `jsonValue` _string_ | JSONValue is a JSON-encoded value that will be parsed and sent as a native JSON<br />object to the Config Flow API. Use this for fields that expect a dictionary or<br />array (e.g. location: '\{"latitude": 54.17, "longitude": 18.55\}'). |  | Optional: \{\} <br /> |
| `secretKeyRef` _[IntegrationSecretKeyRef](#integrationsecretkeyref)_ | SecretKeyRef references a key in a Kubernetes Secret |  | Optional: \{\} <br /> |


#### IssuerReference



IssuerReference references a cert-manager Issuer or ClusterIssuer. The
operator only references issuers — it never creates application issuers.



_Appears in:_
- [GatewaySpec](#gatewayspec)
- [IngressTLSSpec](#ingresstlsspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Issuer/ClusterIssuer. |  |  |
| `kind` _string_ | Kind of the issuer. | Issuer | Enum: [Issuer ClusterIssuer] <br />Optional: \{\} <br /> |
| `group` _string_ | Group of the issuer API. | cert-manager.io | Optional: \{\} <br /> |


#### LocationConfig



LocationConfig defines location settings for Home Assistant onboarding



_Appears in:_
- [BootstrapSpec](#bootstrapspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the location name (e.g., "Home", "Warsaw") |  | Optional: \{\} <br /> |
| `latitude` _string_ | Latitude in decimal degrees (e.g., "52.2297") |  | Pattern: `^-?([0-8]?[0-9](\.[0-9]+)?\|90(\.0+)?)$` <br />Optional: \{\} <br /> |
| `longitude` _string_ | Longitude in decimal degrees (e.g., "21.0122") |  | Pattern: `^-?(1[0-7][0-9](\.[0-9]+)?\|[0-9]\{1,2\}(\.[0-9]+)?\|180(\.0+)?)$` <br />Optional: \{\} <br /> |
| `elevation` _integer_ | Elevation in meters |  | Optional: \{\} <br /> |
| `unitSystem` _string_ | UnitSystem defines the unit system ("metric" or "us_customary") | metric | Enum: [metric us_customary] <br />Optional: \{\} <br /> |
| `currency` _string_ | Currency is the ISO 4217 currency code (e.g., "USD", "EUR", "PLN") |  | Optional: \{\} <br /> |
| `timeZone` _string_ | TimeZone is the IANA timezone (e.g., "Europe/Warsaw", "America/New_York")<br />If not specified, uses spec.timezone |  | Optional: \{\} <br /> |


#### LoggerConfig



LoggerConfig defines logging component configuration



_Appears in:_
- [HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `defaultLevel` _string_ | DefaultLevel is the default logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL) | INFO | Optional: \{\} <br /> |
| `logs` _object (keys:string, values:string)_ | Logs is a map of component names to their logging levels<br />Example: \{"homeassistant.core": "DEBUG", "homeassistant.components.mqtt": "DEBUG"\} |  | Optional: \{\} <br /> |


#### MQTTConfig



MQTTConfig defines MQTT component configuration



_Appears in:_
- [HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `broker` _string_ | Broker is the MQTT broker address (e.g., "mqtt://localhost:1883") |  | Required: \{\} <br /> |
| `username` _string_ | Username for MQTT authentication |  | Optional: \{\} <br /> |
| `passwordRef` _[SecretKeySelector](#secretkeyselector)_ | PasswordRef references a Secret containing the MQTT password<br />The Secret should have a "password" key |  | Optional: \{\} <br /> |
| `clientID` _string_ | ClientID for MQTT connection |  | Optional: \{\} <br /> |
| `keepAlive` _integer_ | KeepAlive defines MQTT keep-alive interval in seconds | 60 | Optional: \{\} <br /> |


#### NetworkPolicyAlphaSpec



NetworkPolicyAlphaSpec configures the (alpha) NetworkPolicy created for the
Home Assistant pod.



_Appears in:_
- [AlphaSpec](#alphaspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls whether the operator creates a NetworkPolicy for the<br />Home Assistant pod, restricting ingress to the operator's namespace and<br />the Home Assistant namespace on the Service port. Egress is left<br />unrestricted (Home Assistant needs broad, unpredictable egress to IoT<br />devices, cloud APIs, and MQTT brokers).<br />NetworkPolicy operates on pod IPs — it does not restrict traffic<br />arriving via the host network interface. Combining this with<br />spec.hostNetwork: true gives only partial isolation.<br />Deliberately without omitempty: this field represents explicit user<br />intent, and the spec.alpha lifecycle plans to flip its default to true<br />in a later phase — omitempty would let an explicit false be dropped and<br />silently re-defaulted to true by the API server once that happens. | false | Optional: \{\} <br /> |


#### RecorderConfig



RecorderConfig defines recorder (database) component configuration



_Appears in:_
- [HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enabled` _boolean_ | Enabled controls if the recorder is enabled | true | Optional: \{\} <br /> |
| `database` _string_ | Database URL for the recorder (e.g., "postgresql://user:pass@host/db")<br />If not specified, uses SQLite with default path.<br />Mutually exclusive with DatabaseSecretRef; DatabaseSecretRef takes precedence. |  | Optional: \{\} <br /> |
| `databaseSecretRef` _[SecretKeySelector](#secretkeyselector)_ | DatabaseSecretRef references a Secret containing the database URL.<br />The resolved value is written as plain text into configuration.yaml,<br />avoiding the !secret tag which is stripped by the YAML round-trip.<br />Takes precedence over Database if both are set.<br />The Secret must be in the same namespace as the HomeAssistant CR. |  | Optional: \{\} <br /> |
| `purgeKeepDays` _integer_ | PurgeKeepDays specifies how many days of history to keep | 30 | Minimum: 1 <br />Optional: \{\} <br /> |


#### SceneEntity



SceneEntity represents a single entity (device) in a scene with its desired state



_Appears in:_
- [HomeAssistantSceneSpec](#homeassistantscenespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `entity_id` _string_ | EntityID is the Home Assistant entity identifier in format domain.object_id<br />Examples: light.living_room, switch.fan, climate.bedroom |  | Pattern: `^[a-z_]+\.[a-z0-9_]+$` <br />Required: \{\} <br /> |
| `state` _string_ | State is the desired state for this entity<br />Examples: "on", "off", numeric values |  | Required: \{\} <br /> |
| `attributes` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#rawextension-runtime-pkg)_ | Attributes contains additional entity-specific attributes<br />Examples: brightness, color_temp, rgb_color for lights<br />This is a flexible structure that accepts any valid Home Assistant entity attributes |  | Type: object <br />Optional: \{\} <br /> |


#### SchedulingSpec



SchedulingSpec declares Kubernetes-native pod scheduling constraints for
the Home Assistant pod. Every field is optional and copied verbatim onto
the generated StatefulSet's pod template; leaving all of them unset
preserves today's freely-schedulable, default-priority behavior.



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `nodeSelector` _object (keys:string, values:string)_ | NodeSelector restricts the pod to nodes matching all of these labels. |  | Optional: \{\} <br /> |
| `affinity` _[Affinity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#affinity-v1-core)_ | Affinity declares node affinity/anti-affinity and pod<br />affinity/anti-affinity rules, using Kubernetes' own Affinity semantics<br />unchanged. Both node-level placement (e.g. "prefer nodes with local<br />NVMe storage") and pod-level positioning relative to other workloads<br />(e.g. "never share a node with this other deployment") are expressed<br />through this single field, matching how corev1.Affinity itself groups<br />NodeAffinity/PodAffinity/PodAntiAffinity together. |  | Optional: \{\} <br /> |
| `tolerations` _[Toleration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#toleration-v1-core) array_ | Tolerations allows the pod to be scheduled onto nodes with matching<br />taints that would otherwise repel it. |  | Optional: \{\} <br /> |
| `priorityClassName` _string_ | PriorityClassName assigns a PriorityClass to the pod, influencing<br />scheduling preemption and eviction order under resource contention.<br />Must name an existing PriorityClass — validated at admission time. |  | Optional: \{\} <br /> |


#### ScriptAction



ScriptAction defines an action to be executed by the script
This is a flexible structure that accepts any valid Home Assistant action configuration



_Appears in:_
- [HomeAssistantScriptSpec](#homeassistantscriptspec)



#### ScriptField



ScriptField defines an input parameter for the script
This is a flexible structure that accepts any valid Home Assistant field configuration



_Appears in:_
- [HomeAssistantScriptSpec](#homeassistantscriptspec)



#### ScriptMode

_Underlying type:_ _string_

ScriptMode defines how the script should behave when called again while already running

_Validation:_
- Enum: [single restart queued parallel]

_Appears in:_
- [HomeAssistantScriptSpec](#homeassistantscriptspec)

| Field | Description |
| --- | --- |
| `single` | ScriptModeSingle - Do not start a new run, issue a warning<br /> |
| `restart` | ScriptModeRestart - Stop previous runs and start a new one<br /> |
| `queued` | ScriptModeQueued - Queue runs in order, sequential execution guaranteed<br /> |
| `parallel` | ScriptModeParallel - Launch independent concurrent runs<br /> |


#### SecretKeyReference



SecretKeyReference defines a reference to a specific key in a Kubernetes Secret.



_Appears in:_
- [HomeAssistantSecretsSpec](#homeassistantsecretsspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret |  | Required: \{\} <br /> |
| `keys` _string array_ | Keys to extract from the Secret. If empty, all keys will be included. |  | Optional: \{\} <br /> |


#### SecretKeySelector



SecretKeySelector selects a Secret and an optional key



_Appears in:_
- [MQTTConfig](#mqttconfig)
- [RecorderConfig](#recorderconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret |  | Required: \{\} <br /> |
| `key` _string_ | Key in the Secret (if not specified, defaults to "password" or "value") |  | Optional: \{\} <br /> |


#### SecretReference



SecretReference references a Secret for sensitive data



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the Secret |  |  |


#### ServiceSpec



ServiceSpec defines how Home Assistant is exposed within the cluster.



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `type` _[ServiceType](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#servicetype-v1-core)_ | Type of Kubernetes Service (ClusterIP, NodePort, LoadBalancer) | ClusterIP | Enum: [ClusterIP NodePort LoadBalancer] <br />Optional: \{\} <br /> |
| `port` _integer_ | Port for the Home Assistant web UI | 8123 | Optional: \{\} <br /> |
| `nodePort` _integer_ | NodePort for NodePort service type (optional, auto-assigned if not set) |  | Optional: \{\} <br /> |


#### StorageSpec



StorageSpec defines storage configuration for Home Assistant.



_Appears in:_
- [HomeAssistantSpec](#homeassistantspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `size` _[Quantity](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#quantity-resource-api)_ | Size of the persistent volume (e.g., "5Gi", "10Gi") | 5Gi | Optional: \{\} <br /> |
| `storageClassName` _string_ | StorageClassName for the PVC. If empty, uses cluster default. |  | Optional: \{\} <br /> |
| `accessMode` _[PersistentVolumeAccessMode](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#persistentvolumeaccessmode-v1-core)_ | AccessMode for the PVC | ReadWriteOnce | Optional: \{\} <br /> |
| `retainPVC` _boolean_ | RetainPVC controls whether the PVC survives deletion of the HomeAssistant CR.<br />When true, no ownerReference is set on the PVC — it will not be garbage-collected<br />when the CR is deleted (e.g. by FluxCD reconciliation), preventing accidental data loss.<br />When false (default), the PVC is owned by the CR and deleted together with it. | false | Optional: \{\} <br /> |
| `initContainer` _[InitContainerSpec](#initcontainerspec)_ | InitContainer configures the init container that pre-creates required YAML files<br />(automations.yaml, scenes.yaml, scripts.yaml) on the PVC before Home Assistant starts.<br />This prevents HA from entering recovery mode when the !include directives are present<br />but the files do not yet exist. |  | Optional: \{\} <br /> |



## ha.homeassistant.io/v1alpha1

Package v1alpha1 contains API Schema definitions for the ha v1alpha1 API group.
Resources in this package are experimental: fields may change shape or be
removed in a minor release, with no deprecation period. That is the trade-off
for shipping a capability whose real-world behaviour cannot be settled by
tests alone; it graduates to a stable group once it has been exercised on
hardware the maintainers do not have.

### Resource Types
- [HomeAssistantCommunityRepository](#homeassistantcommunityrepository)
- [HomeAssistantCommunityRepositoryList](#homeassistantcommunityrepositorylist)



#### CommunityRepositoryCategory

_Underlying type:_ _string_

CommunityRepositoryCategory is a HACS repository category. Values match HACS's own
hacs.json "category" field exactly.

_Validation:_
- Enum: [integration plugin theme python_script template]

_Appears in:_
- [HomeAssistantCommunityRepositorySpec](#homeassistantcommunityrepositoryspec)

| Field | Description |
| --- | --- |
| `integration` |  |
| `plugin` |  |
| `theme` |  |
| `python_script` |  |
| `template` |  |


#### CommunityRepositoryPhase

_Underlying type:_ _string_

CommunityRepositoryPhase is the reconciliation lifecycle phase of a
HomeAssistantCommunityRepository.



_Appears in:_
- [HomeAssistantCommunityRepositoryStatus](#homeassistantcommunityrepositorystatus)

| Field | Description |
| --- | --- |
| `Pending` |  |
| `Validating` |  |
| `Installing` |  |
| `Installed` |  |
| `Failed` |  |
| `Removing` |  |


#### HomeAssistantCommunityRepository



HomeAssistantCommunityRepository installs a HACS-compatible community extension
(integration, plugin, theme, python_script, or template) into an existing
HomeAssistant instance, without requiring HACS or its UI to be present. This is an
EXPERIMENTAL, alpha-quality resource: it carries no API stability guarantee between
releases.



_Appears in:_
- [HomeAssistantCommunityRepositoryList](#homeassistantcommunityrepositorylist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantCommunityRepository` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantCommunityRepositorySpec](#homeassistantcommunityrepositoryspec)_ |  |  |  |
| `status` _[HomeAssistantCommunityRepositoryStatus](#homeassistantcommunityrepositorystatus)_ |  |  |  |


#### HomeAssistantCommunityRepositoryList



HomeAssistantCommunityRepositoryList contains a list of HomeAssistantCommunityRepository





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantCommunityRepositoryList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantCommunityRepository](#homeassistantcommunityrepository) array_ |  |  |  |


#### HomeAssistantCommunityRepositorySpec



HomeAssistantCommunityRepositorySpec defines the desired state of HomeAssistantCommunityRepository



_Appears in:_
- [HomeAssistantCommunityRepository](#homeassistantcommunityrepository)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | HomeAssistantRef references the HomeAssistant instance to install this repository into |  | Required: \{\} <br /> |
| `category` _[CommunityRepositoryCategory](#communityrepositorycategory)_ | Category is the HACS repository category. appdaemon and netdaemon are intentionally<br />not accepted here: they require a separate runtime this operator does not deploy. |  | Enum: [integration plugin theme python_script template] <br />Required: \{\} <br /> |
| `repository` _string_ | Repository is the GitHub "owner/repo" shorthand (not a full URL). |  | Pattern: `^[\w.-]+/[\w.-]+$` <br />Required: \{\} <br /> |
| `ref` _string_ | Ref is the tag, branch, or commit SHA to install. Pinned and explicit — this<br />operator never tracks a "latest" release automatically. Restricted to<br />characters valid in a git ref: word characters, dot, dash and slash, so<br />branch names such as release/v1 are accepted. URL-reserved characters that<br />could alter the codeload request path are not. |  | MinLength: 1 <br />Pattern: `^[\w][\w.\-/]*$` <br />Required: \{\} <br /> |


#### HomeAssistantCommunityRepositoryStatus



HomeAssistantCommunityRepositoryStatus defines the observed state of HomeAssistantCommunityRepository



_Appears in:_
- [HomeAssistantCommunityRepository](#homeassistantcommunityrepository)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[CommunityRepositoryPhase](#communityrepositoryphase)_ | Phase is the current lifecycle phase. |  | Optional: \{\} <br /> |
| `installedVersion` _string_ | InstalledVersion is the last ref that was successfully validated and activated.<br />It does NOT change until a newly requested ref is fully confirmed, so a failed<br />update never reports a broken version as installed. |  | Optional: \{\} <br /> |
| `resolvedTarget` _string_ | ResolvedTarget is the install target computed from the source repository's own<br />manifest (the integration domain, or the theme/script/template/plugin file name).<br />Together with the referenced HomeAssistant name and Category it forms the<br />conflict-detection key, so the same target may be installed into two<br />different instances. |  | Optional: \{\} <br /> |
| `lastError` _string_ | LastError contains a human-readable error message from the last failed operation.<br />Cleared when the operation succeeds. |  | Optional: \{\} <br /> |
| `installingSince` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | InstallingSince records when the resource most recently entered the<br />Installing phase. Used to bound the activation retry window; cleared when<br />the resource leaves Installing (Installed or Failed). |  | Optional: \{\} <br /> |
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed CR |  | Optional: \{\} <br /> |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | Conditions represent the latest available observations of the repository state |  | Optional: \{\} <br /> |


#### HomeAssistantReference



HomeAssistantReference references the HomeAssistant instance a community repository
targets. Declared locally in v1alpha1 (rather than reusing api/v1's type) so this
experimental API group-version has no coupling to the stable v1 types.



_Appears in:_
- [HomeAssistantCommunityRepositorySpec](#homeassistantcommunityrepositoryspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name of the HomeAssistant resource |  | MinLength: 1 <br />Required: \{\} <br /> |
