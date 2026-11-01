package controller

// The operator creates narrowly scoped, per-instance writer identities during
// synchronization setup. The generated ClusterRole is only the authority to
// create and maintain those identities; it is not the runtime writer role.
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantbackups;homeassistantrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantbackups/status;homeassistantrestores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantbackups/finalizers;homeassistantrestores/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
