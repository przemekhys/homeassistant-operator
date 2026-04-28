# API Reference

## Packages
- [ha.homeassistant.io/v1alpha1](#hahomeassistantiov1alpha1)


## ha.homeassistant.io/v1alpha1

Package v1alpha1 contains API Schema definitions for the ha v1alpha1 API group.

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
| `apiTokenReady` _boolean_ | ApiTokenReady indicates whether the API token has been created and stored |  | Optional: \{\} <br /> |
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


#### HomeAssistant



HomeAssistant is the Schema for the homeassistants API.



_Appears in:_
- [HomeAssistantList](#homeassistantlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantArea` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[HomeAssistantAreaSpec](#homeassistantareaspec)_ |  |  | Required: \{\} <br /> |
| `status` _[HomeAssistantAreaStatus](#homeassistantareastatus)_ |  |  | Optional: \{\} <br /> |


#### HomeAssistantAreaList



HomeAssistantAreaList contains a list of HomeAssistantArea





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | conditions represent the current state of the HomeAssistantArea resource |  | Optional: \{\} <br /> |


#### HomeAssistantAutomation



HomeAssistantAutomation is the Schema for the homeassistantautomations API



_Appears in:_
- [HomeAssistantAutomationList](#homeassistantautomationlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantAutomation` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantAutomationSpec](#homeassistantautomationspec)_ |  |  |  |
| `status` _[HomeAssistantAutomationStatus](#homeassistantautomationstatus)_ |  |  |  |


#### HomeAssistantAutomationList



HomeAssistantAutomationList contains a list of HomeAssistantAutomation





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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
| `id` _string_ | ID is a unique identifier for the automation (used by Home Assistant)<br />If not specified, will be auto-generated from the CR name |  | Optional: \{\} <br /> |
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
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantConfiguration` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)_ |  |  |  |
| `status` _[HomeAssistantConfigurationStatus](#homeassistantconfigurationstatus)_ |  |  |  |


#### HomeAssistantConfigurationList



HomeAssistantConfigurationList contains a list of HomeAssistantConfiguration.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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


#### HomeAssistantFloor



HomeAssistantFloor is the Schema for the homeassistantfloors API



_Appears in:_
- [HomeAssistantFloorList](#homeassistantfloorlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantFloor` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[HomeAssistantFloorSpec](#homeassistantfloorspec)_ |  |  | Required: \{\} <br /> |
| `status` _[HomeAssistantFloorStatus](#homeassistantfloorstatus)_ |  |  | Optional: \{\} <br /> |


#### HomeAssistantFloorList



HomeAssistantFloorList contains a list of HomeAssistantFloor





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | conditions represent the current state of the HomeAssistantFloor resource |  | Optional: \{\} <br /> |


#### HomeAssistantIntegration



HomeAssistantIntegration manages a Home Assistant integration (config entry) via the Config Flow API.
It supports create, adopt, reconfigure (delete+re-create on spec change), and cleanup via finalizer.
Only single-step Config Flows are supported (e.g. mqtt, recorder, esphome).



_Appears in:_
- [HomeAssistantIntegrationList](#homeassistantintegrationlist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantIntegration` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantIntegrationSpec](#homeassistantintegrationspec)_ |  |  |  |
| `status` _[HomeAssistantIntegrationStatus](#homeassistantintegrationstatus)_ |  |  |  |


#### HomeAssistantIntegrationList



HomeAssistantIntegrationList contains a list of HomeAssistantIntegration





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantIntegrationList` | | |
| `metadata` _[ListMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#listmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `items` _[HomeAssistantIntegration](#homeassistantintegration) array_ |  |  |  |


#### HomeAssistantIntegrationSpec



HomeAssistantIntegrationSpec defines the desired state of HomeAssistantIntegration



_Appears in:_
- [HomeAssistantIntegration](#homeassistantintegration)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `homeAssistantRef` _[HomeAssistantReference](#homeassistantreference)_ | HomeAssistantRef references the HomeAssistant instance to configure |  |  |
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
| `observedGeneration` _integer_ | ObservedGeneration reflects the generation of the most recently observed CR |  | Optional: \{\} <br /> |


#### HomeAssistantLabel



HomeAssistantLabel is the Schema for the homeassistantlabels API



_Appears in:_
- [HomeAssistantLabelList](#homeassistantlabellist)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantLabel` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  | Optional: \{\} <br /> |
| `spec` _[HomeAssistantLabelSpec](#homeassistantlabelspec)_ |  |  | Required: \{\} <br /> |
| `status` _[HomeAssistantLabelStatus](#homeassistantlabelstatus)_ |  |  | Optional: \{\} <br /> |


#### HomeAssistantLabelList



HomeAssistantLabelList contains a list of HomeAssistantLabel





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#condition-v1-meta) array_ | conditions represent the current state of the HomeAssistantLabel resource |  | Optional: \{\} <br /> |


#### HomeAssistantList



HomeAssistantList contains a list of HomeAssistant.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantScene` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantSceneSpec](#homeassistantscenespec)_ |  |  |  |
| `status` _[HomeAssistantSceneStatus](#homeassistantscenestatus)_ |  |  |  |


#### HomeAssistantSceneList



HomeAssistantSceneList contains a list of HomeAssistantScene





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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
| `id` _string_ | ID is a unique identifier for the scene (used by Home Assistant)<br />If not specified, will be auto-generated from the CR name |  | Optional: \{\} <br /> |
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
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantScript` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantScriptSpec](#homeassistantscriptspec)_ |  |  |  |
| `status` _[HomeAssistantScriptStatus](#homeassistantscriptstatus)_ |  |  |  |


#### HomeAssistantScriptList



HomeAssistantScriptList contains a list of HomeAssistantScript





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
| `kind` _string_ | `HomeAssistantSecrets` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[HomeAssistantSecretsSpec](#homeassistantsecretsspec)_ |  |  |  |
| `status` _[HomeAssistantSecretsStatus](#homeassistantsecretsstatus)_ |  |  |  |


#### HomeAssistantSecretsList



HomeAssistantSecretsList contains a list of HomeAssistantSecrets.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `ha.homeassistant.io/v1alpha1` | | |
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


#### HomeAssistantSpec



HomeAssistantSpec defines the desired state of HomeAssistant.



_Appears in:_
- [HomeAssistant](#homeassistant)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version is the Home Assistant version/tag to deploy (e.g., "2024.1.0", "stable", "latest") | stable | Optional: \{\} <br /> |
| `image` _string_ | Image allows overriding the default Home Assistant image | ghcr.io/home-assistant/home-assistant | Optional: \{\} <br /> |
| `storage` _[StorageSpec](#storagespec)_ | Storage configuration for Home Assistant data |  | Optional: \{\} <br /> |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#resourcerequirements-v1-core)_ | Resources defines CPU and memory requests/limits |  | Optional: \{\} <br /> |
| `service` _[ServiceSpec](#servicespec)_ | Service configuration for exposing Home Assistant |  | Optional: \{\} <br /> |
| `ingress` _[IngressSpec](#ingressspec)_ | Ingress configuration for external access |  | Optional: \{\} <br /> |
| `timezone` _string_ | Timezone for the Home Assistant instance (e.g., "Europe/Warsaw") | UTC | Optional: \{\} <br /> |
| `secretsFrom` _[SecretReference](#secretreference)_ | SecretsFrom references a Secret containing secrets.yaml<br />The Secret should have a key "secrets.yaml" with the HA secrets |  | Optional: \{\} <br /> |
| `hostNetwork` _boolean_ | HostNetwork enables host networking for the Home Assistant pod.<br />When true, the pod uses the host's network namespace, enabling discovery<br />of IoT devices via mDNS, SSDP, and DHCP on the local network. |  | Optional: \{\} <br /> |
| `bootstrap` _[BootstrapSpec](#bootstrapspec)_ | Bootstrap configures automatic onboarding and API token creation<br />When enabled, the operator will automatically complete the Home Assistant<br />onboarding process and create a long-lived access token for API access |  | Optional: \{\} <br /> |
| `backup` _[BackupSpec](#backupspec)_ | Backup configures automatic backups using Home Assistant's built-in backup system.<br />Requires bootstrap with API token enabled. |  | Optional: \{\} <br /> |


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
| `bootstrap` _[BootstrapStatus](#bootstrapstatus)_ | BootstrapStatus contains the status of the automatic bootstrap process |  | Optional: \{\} <br /> |
| `selfUnbanCount` _integer_ | SelfUnbanCount is the number of times the operator has removed its own IP<br />from HA's ip_bans.yaml and restarted the pod to clear in-memory bans. |  | Optional: \{\} <br /> |
| `lastSelfUnban` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v/#time-v1-meta)_ | LastSelfUnban is the timestamp of the most recent self-unban operation. |  | Optional: \{\} <br /> |


#### HTTPConfig



HTTPConfig defines HTTP component configuration



_Appears in:_
- [HomeAssistantConfigurationSpec](#homeassistantconfigurationspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `corsDomains` _string array_ | CorsDomains is a list of allowed CORS origins |  | Optional: \{\} <br /> |
| `trustProxy` _boolean_ | TrustProxy enables trust in X-Forwarded-For header |  | Optional: \{\} <br /> |
| `useXForwardedFor` _boolean_ | UseXForwardedFor enables usage of X-Forwarded-For header |  | Optional: \{\} <br /> |


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
| `secretName` _string_ | SecretName containing the TLS certificate. If empty, cert-manager annotation should be used. |  | Optional: \{\} <br /> |


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


#### LocationConfig



LocationConfig defines location settings for Home Assistant onboarding



_Appears in:_
- [BootstrapSpec](#bootstrapspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the location name (e.g., "Home", "Warsaw") |  | Optional: \{\} <br /> |
| `latitude` _string_ | Latitude in decimal degrees (e.g., "52.237703") |  | Pattern: `^-?([0-8]?[0-9](\.[0-9]+)?\|90(\.0+)?)$` <br />Optional: \{\} <br /> |
| `longitude` _string_ | Longitude in decimal degrees (e.g., "20.989075") |  | Pattern: `^-?(1[0-7][0-9](\.[0-9]+)?\|[0-9]\{1,2\}(\.[0-9]+)?\|180(\.0+)?)$` <br />Optional: \{\} <br /> |
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
