// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

const (
	APIVersion             = "kairon.zyvor.dev/v1alpha1"
	KindMachine            = "Machine"
	KindMachineMigration   = "MachineMigration"
	KindMachineSnapshot    = "MachineSnapshot"
	Finalizer              = "kairon.zyvor.dev/runtime-cleanup"
	CapableLabel           = "kairon.zyvor.dev/capable"
	DefaultNamespace       = "default"
	AnnotationAdoptOnly    = "kairon.zyvor.dev/adopt-only"
	AnnotationVFIOBDF      = "kairon.zyvor.dev/vfio-bdf"
	AnnotationMigrationRef = "kairon.zyvor.dev/migration"
	// AnnotationConsoleAllowedUsers opts a Machine into a per-Machine VNC
	// console access allowlist -- a comma-separated list of kairon-ui
	// operator usernames (see internal/uiapi's consoleAuthorized). Unset
	// (the default) means every authenticated operator may open this
	// Machine's console, exactly as before this annotation existed --
	// this is opt-in, never a behavior change for a Machine that doesn't
	// set it.
	AnnotationConsoleAllowedUsers = "kairon.zyvor.dev/console-allowed-users"
	// AnnotationCordonEvacuateAttemptedAt records the last time
	// kairon-controller's opt-in cordon-evacuation reconcile step (see
	// internal/controller/cordon.go) created, or tried and was
	// disruption-budget-blocked from creating, a MachineMigration for
	// this Machine because its node was cordoned -- a per-Machine
	// cooldown so a controller reconciling every few seconds doesn't
	// create (or attempt) a new MachineMigration every single tick for a
	// Machine a budget is currently blocking. RFC3339 UTC.
	AnnotationCordonEvacuateAttemptedAt = "kairon.zyvor.dev/cordon-evacuate-attempted-at"
	// AnnotationQuiesceRequest/AnnotationQuiesceStatus are a request/
	// response pair kairon-controller and kairon-node use to coordinate
	// guest filesystem quiesce (real guest-fsfreeze/-thaw) around a
	// MachineSnapshot -- kairon-controller has no direct network path to
	// a Machine's FluxVM instance (only the node it's scheduled on does),
	// so this is the same "durable request in the API, not memory"
	// pattern AnnotationAdoptOnly/AnnotationMigrationRef already
	// establish for controller<->node coordination, rather than a new
	// RPC of its own. See internal/controller/snapshot.go and
	// internal/agent/quiesce.go.
	//
	// AnnotationQuiesceRequest is "<MachineSnapshot name>@<RFC3339
	// request time>", set by kairon-controller on the Machine to ask its
	// node to freeze the guest for that specific snapshot (the timestamp
	// lets the controller give up waiting after a timeout and fall back
	// to a crash-consistent snapshot, without needing a separate status
	// field to track it), and cleared (removed) by kairon-controller once
	// it's done capturing the snapshot, as the request to thaw.
	// AnnotationQuiesceStatus is the same "<name>@<time>" value, set by
	// kairon-node once it has confirmed the freeze actually happened, and
	// removed by kairon-node once it has confirmed the thaw did.
	// kairon-node treats "request present, status doesn't match" as
	// "freeze this," and "request absent, status still present" as "thaw
	// this" -- retried every reconcile tick, indefinitely, for thaw in
	// particular: a stuck-frozen guest filesystem is a real, guest-visible
	// failure mode kairon-node must never silently abandon.
	AnnotationQuiesceRequest = "kairon.zyvor.dev/quiesce-request"
	AnnotationQuiesceStatus  = "kairon.zyvor.dev/quiesce-status"
	// ConditionNodeUnreachable is a MachineStatus.Conditions[].Type
	// kairon-controller sets/clears every reconcile tick to reflect
	// whether spec.nodeName currently names a Ready, present Kubernetes
	// Node -- see internal/controller/fencing.go. Detection only: nothing
	// automatically reschedules the Machine when this goes True (that
	// would risk running the same VM twice if the node isn't actually
	// dead, just unreachable) -- see ConditionFenced and
	// `kaironctl fence`.
	ConditionNodeUnreachable = "NodeUnreachable"
	// ConditionFenced records an operator-attested `kaironctl fence`
	// action: the operator has confirmed (out-of-band, e.g. power-off)
	// that the node named in Reason/Message is truly gone, not just
	// unreachable, and it's safe to let the Machine be rescheduled
	// elsewhere. Kairon cannot verify this itself.
	ConditionFenced = "Fenced"
)

type TypeMeta struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

type ObjectMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	UID               string            `json:"uid,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	Finalizers        []string          `json:"finalizers,omitempty"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
}

type Machine struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta    `json:"metadata"`
	Spec     MachineSpec   `json:"spec"`
	Status   MachineStatus `json:"status,omitempty"`
}

type MachineList struct {
	TypeMeta `json:",inline"`
	Items    []Machine `json:"items"`
}

type MachineSpec struct {
	NodeName string `json:"nodeName,omitempty"`
	// InstanceTypeName optionally names a MachineInstanceType (same
	// namespace) kairon-controller resolves into Resources exactly once
	// -- see internal/controller/instancetype.go and
	// docs/guides/machine-instance-types.md. Only takes effect the first
	// time this is set while Resources is still empty; editing either
	// field afterward never re-resolves, matching every other
	// creation-time-only field in this project.
	InstanceTypeName string                 `json:"instanceTypeName,omitempty"`
	Image            ImageSpec              `json:"image"`
	Resources        ResourceSpec           `json:"resources"`
	Runtime          RuntimeSpec            `json:"runtime,omitempty"`
	Network          NetworkSpec            `json:"network,omitempty"`
	CloudInit        CloudInitSpec          `json:"cloudInit,omitempty"`
	ServiceFabric    ServiceFabricSpec      `json:"serviceFabric,omitempty"`
	PowerState       string                 `json:"powerState,omitempty"`
	Tenant           string                 `json:"tenant,omitempty"`
	TTLSeconds       int64                  `json:"ttlSeconds,omitempty"`
	Placement        PlacementSpec          `json:"placement,omitempty"`
	Security         SecuritySpec           `json:"security,omitempty"`
	Volumes          []MachineVolume        `json:"volumes,omitempty"`
	DeviceClaims     []DeviceClaimReference `json:"deviceClaims,omitempty"`
	GuestAgent       GuestAgentSpec         `json:"guestAgent,omitempty"`
}

// GuestAgentSpec opts a Machine into FluxVM's real qemu-guest-agent
// (virtio-serial) channel -- off by default, since it requires the guest
// image to actually run qemu-guest-agent (e.g. via
// spec.cloudInit.packages) to be useful. Once enabled, kairon-node uses it
// to resolve status.guestIP for network modes with no DHCP lease file to
// parse (spec.network.mode: user/SLIRP in particular) -- see
// docs/guides/machine-guest-agent.md.
type GuestAgentSpec struct {
	Enabled bool `json:"enabled,omitempty"`
}

// CloudInitSpec injects operator-supplied guest customization at first boot,
// forwarded verbatim into FluxVM's own cloud-init NoCloud seed image
// (github.com/zyvorai/fluxvm crates/fluxvm-core/src/model.rs CloudInitSpec).
// Only applied when a Machine's FluxVM runtime is first created
// (internal/agent.reconcileMachine) -- editing it on an existing Machine has
// no effect, same as spec.network.forwards and spec.resources.
type CloudInitSpec struct {
	Hostname          string   `json:"hostname,omitempty"`
	User              string   `json:"user,omitempty"`
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`
	Packages          []string `json:"packages,omitempty"`
	RunCmd            []string `json:"runCmd,omitempty"`
}

type ImageSpec struct {
	// Path is the boot disk's real path on the target node -- required
	// unless Source is set, in which case kairon-node fills this in
	// itself (see internal/agent/imageimport.go) after resolving Source
	// into its local image cache; a Machine author never needs to know
	// or predict that path themselves.
	Path   string       `json:"path,omitempty"`
	Digest string       `json:"digest,omitempty"`
	Source *ImageSource `json:"source,omitempty"`
}

// ImageSource names a golden image kairon-node itself downloads into a
// node-local, digest-keyed cache instead of requiring an operator to have
// already placed a file at Path -- see internal/agent/imageimport.go.
// Digest is mandatory whenever Source is set: it's the cache key, so
// without it two Machines naming the same (mutable) HTTPURL would have no
// way to know whether they mean the same bytes.
type ImageSource struct {
	// HTTPURL is a plain http(s):// URL to a qcow2/raw image file.
	// OCI/container-registry references are a deliberate non-goal --
	// see docs/guides/machine-image-import.md.
	HTTPURL string `json:"httpURL,omitempty"`
}

type ResourceSpec struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
	// Hugepages, NUMANode, and CPUSet are direct, opt-in passthroughs to
	// FluxVM's own existing QEMU-backend-only support for the same
	// (fluxvm-core's CreateVmRequest already has hugepages/numa_node/
	// cpuset -- this is genuinely new plumbing on Kairon's side, not
	// something blocked upstream the way VFIO-through-live-migration
	// was). See docs/guides/machine-cpu-numa.md for exactly what each
	// does and doesn't guarantee, and internal/fluxvm.Client.CreateWithVFIO
	// for the qemu-backend-only enforcement.
	//
	// Deliberately no dedicatedCpuPlacement/exclusive-host-core-pinning
	// field here -- FluxVM separately supports real host cgroup cpuset
	// pinning (its own resize/ResourcePatch.cpuset_cpus), but allocating
	// *specific*, non-overlapping host CPU numbers across every Machine
	// competing for them on one node is a real capacity-allocator problem
	// (the same shape VFIODevicesLabel's own doc comment already flags as
	// out of scope for this project's current "no capacity model at all"
	// scheduler) -- a bigger, separate design, not attempted here.
	Hugepages bool   `json:"hugepages,omitempty"`
	NUMANode  *int   `json:"numaNode,omitempty"`
	CPUSet    string `json:"cpuSet,omitempty"`
}

type RuntimeSpec struct {
	Backend string `json:"backend,omitempty"`
	Kernel  string `json:"kernel,omitempty"`
}

type PlacementSpec struct {
	Architecture string            `json:"architecture,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Affinity/AntiAffinity are required (hard) constraints -- filters
	// consulted by internal/scheduler.Scheduler.eligible against every
	// other currently-scheduled Machine; a node failing one of these is
	// never a candidate at all. PreferredAffinity/PreferredAntiAffinity and
	// TopologySpreadConstraints below are soft: they only ever influence
	// which *eligible* node wins via internal/scheduler's weighted scoring,
	// they can never reject a node outright.
	Affinity     []MachineAffinityTerm `json:"affinity,omitempty"`
	AntiAffinity []MachineAffinityTerm `json:"antiAffinity,omitempty"`
	// PreferredAffinity/PreferredAntiAffinity add/subtract each term's
	// Weight to a candidate node's score when satisfied -- see
	// internal/scheduler.score. A Machine with none of these set schedules
	// identically to before this field existed (least-loaded, deterministic
	// hash tie-break): the scoring pass degenerates to exactly that when
	// there's nothing to prefer.
	PreferredAffinity     []WeightedAffinityTerm `json:"preferredAffinity,omitempty"`
	PreferredAntiAffinity []WeightedAffinityTerm `json:"preferredAntiAffinity,omitempty"`
	// TopologySpreadConstraints favors, for each constraint, whichever
	// eligible node's TopologyKey-domain currently has the fewest other
	// Machines matching LabelSelector -- this scoring pass (see
	// internal/scheduler.topologySpreadPenalty) applies to every
	// constraint regardless of WhenUnsatisfiable, mirroring how a real
	// Kubernetes topology-spread scoring plugin doesn't care about that
	// field either. WhenUnsatisfiable: "DoNotSchedule" additionally
	// hard-filters: a node is never a candidate at all if placing this
	// Machine there would push that constraint's skew over MaxSkew (see
	// internal/scheduler.filterMaxSkew) -- the default, "" (equivalent to
	// "ScheduleAnyway"), keeps every existing topologySpreadConstraints
	// spec scheduling identically to before this field existed: scoring
	// only, never a hard rejection.
	TopologySpreadConstraints []TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

// MachineAffinityTerm is satisfied when at least one (Affinity) / no
// (AntiAffinity) other Machine matching LabelSelector currently sits on a
// node sharing the candidate node's value for the TopologyKey label
// (e.g. "kubernetes.io/hostname" for same/different-node, or a rack/zone
// label). Mirrors the shape of Kubernetes Pod affinity terms closely enough
// to be immediately familiar, deliberately not reinvented.
type MachineAffinityTerm struct {
	LabelSelector map[string]string `json:"labelSelector"`
	TopologyKey   string            `json:"topologyKey"`
}

// WeightedAffinityTerm is a MachineAffinityTerm plus a Weight (mirrors
// Kubernetes' own PreferredSchedulingTerm shape): internal/scheduler.score
// adds Weight to a candidate node's score when the term is satisfied (for
// PreferredAffinity) or subtracts it (for PreferredAntiAffinity). Weight
// has no fixed range -- it's compared directly against the scheduler's
// load-balancing penalty (1 point per already-assigned Machine on that
// node), so a Weight of, say, 20 comfortably outweighs a handful of
// existing Machines' worth of load imbalance.
type WeightedAffinityTerm struct {
	Weight              int32 `json:"weight"`
	MachineAffinityTerm `json:",inline"`
}

// TopologySpreadConstraint favors spreading Machines matching
// LabelSelector evenly across the distinct values of the TopologyKey
// label -- always, as a soft scoring signal. WhenUnsatisfiable additionally
// controls whether MaxSkew is hard-enforced -- see PlacementSpec's doc
// comment.
type TopologySpreadConstraint struct {
	TopologyKey   string            `json:"topologyKey"`
	LabelSelector map[string]string `json:"labelSelector"`
	MaxSkew       int32             `json:"maxSkew,omitempty"`
	// WhenUnsatisfiable mirrors the Kubernetes field: "DoNotSchedule" hard-
	// enforces MaxSkew (a node that would exceed it is never eligible);
	// "ScheduleAnyway", or empty/unset (the default, and this project's
	// original behavior), never rejects a node over skew -- MaxSkew only
	// ever influences scoring.
	WhenUnsatisfiable string `json:"whenUnsatisfiable,omitempty"`
}

const (
	WhenUnsatisfiableDoNotSchedule  = "DoNotSchedule"
	WhenUnsatisfiableScheduleAnyway = "ScheduleAnyway"
)

type SecuritySpec struct {
	SecureBoot bool `json:"secureBoot,omitempty"`
	TPM        bool `json:"tpm,omitempty"`
}

type MachineVolume struct {
	Name      string `json:"name"`
	ClaimName string `json:"claimName"`
}

type DeviceClaimReference struct {
	Name string `json:"name"`
}

type MachineStatus struct {
	Phase     string `json:"phase,omitempty"`
	NodeName  string `json:"nodeName,omitempty"`
	RuntimeID string `json:"runtimeID,omitempty"`
	// GuestIP is the single "primary" address (fluxvm.BestGuestIP's pick,
	// or the DHCP-lease address when one exists) -- kept for backward
	// compatibility with existing consumers of this field (e.g. the
	// `kubectl get machine` IP printer column). GuestIPs is the full
	// address list (multi-NIC, IPv4 and IPv6); when non-empty,
	// GuestIPs[0] == GuestIP.
	GuestIP            string                `json:"guestIP,omitempty"`
	GuestIPs           []string              `json:"guestIPs,omitempty"`
	Network            *MachineNetworkStatus `json:"network,omitempty"`
	ObservedGeneration int64                 `json:"observedGeneration,omitempty"`
	Message            string                `json:"message,omitempty"`
	Conditions         []Condition           `json:"conditions,omitempty"`
	// AppliedVCPUs/AppliedMemoryMiB track what Kairon has actually hotplugged
	// into the live FluxVM runtime so far -- FluxVM has no query endpoint for
	// "current live vcpus/memory" (hotplugged CPUs/DIMMs are pure QMP-time
	// state, never persisted back into its own VM record), so Kairon is the
	// only source of truth for how much of spec.resources has been realized.
	// Seeded from spec.resources at creation time; reset whenever the FluxVM
	// runtime is recreated (a stop/start cycle loses every hotplugged
	// resource, since they're not part of the boot-time -smp/-m args).
	AppliedVCPUs     uint32 `json:"appliedVCPUs,omitempty"`
	AppliedMemoryMiB uint64 `json:"appliedMemoryMiB,omitempty"`
	// VolumeStagingPath/VolumePublishPath record that kairon-node has
	// already called NodeStageVolume/NodePublishVolume (see
	// internal/agent/storage.go, internal/csinode) for this Machine's
	// spec.volumes[0] when it's CSI-backed -- kairon-csi-node's Node
	// service has no query for "already staged/published" (NodeStageVolume
	// is idempotent, but calling it every 3s reconcile tick would still
	// mean an iscsiadm/mount syscall every tick for no reason), so Kairon
	// tracks the fact itself in status, the same pattern
	// AppliedVCPUs/AppliedMemoryMiB already use for FluxVM hotplug. Both
	// empty for a hostPath/local-backed volume, or when spec.volumes is
	// unset -- neither of those is ever staged/published, only read
	// directly.
	VolumeStagingPath string `json:"volumeStagingPath,omitempty"`
	VolumePublishPath string `json:"volumePublishPath,omitempty"`
	// VolumeHandle mirrors the CSI volume_id NodeStageVolume was called
	// with -- kept in status (not just re-read from the PV at teardown
	// time) so cleanup can call NodeUnstageVolume correctly even if the
	// PVC/PV has already been deleted by the time the Machine itself
	// finishes tearing down.
	VolumeHandle string `json:"volumeHandle,omitempty"`
}

type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

type MachineMigration struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta             `json:"metadata"`
	Spec     MachineMigrationSpec   `json:"spec"`
	Status   MachineMigrationStatus `json:"status,omitempty"`
}

type MachineMigrationList struct {
	TypeMeta `json:",inline"`
	Items    []MachineMigration `json:"items"`
}

type MachineMigrationSpec struct {
	MachineName     string `json:"machineName"`
	Strategy        string `json:"strategy,omitempty"` // auto|live|cold
	TargetNode      string `json:"targetNode,omitempty"`
	Mode            string `json:"mode,omitempty"` // pre-copy|post-copy
	BandwidthMbps   uint64 `json:"bandwidthMbps,omitempty"`
	MaxDowntimeMs   uint64 `json:"maxDowntimeMs,omitempty"`
	MultifdChannels uint8  `json:"multifdChannels,omitempty"`
	// MigrationNetwork names a migration network configured on the
	// destination node's adapter (--migration-network name=ip), used to
	// select which address the RAM/state transfer binds/advertises.
	// Empty uses the adapter's default advertise address.
	MigrationNetwork string `json:"migrationNetwork,omitempty"`
	// Recovery is the operator's explicit, attested instruction for
	// resolving a migration parked in NeedsRecovery -- never set by Kairon
	// itself, and never acted on unless AcknowledgedDiagnosis matches
	// Action (see internal/agent's reconcileNeedsRecovery). Left unset
	// (nil), a NeedsRecovery migration stays parked indefinitely with no
	// automatic resolution, by design.
	Recovery *MachineMigrationRecoverySpec `json:"recovery,omitempty"`
}

// MachineMigrationRecoverySpec is the operator's attested recovery
// instruction: what to do (Action), what they observed that justifies it
// (AcknowledgedDiagnosis), and why (Reason, free text). All three are
// required together -- reconcileNeedsRecovery refuses to act if
// AcknowledgedDiagnosis doesn't match Action, or Reason is empty, rather
// than silently guessing.
type MachineMigrationRecoverySpec struct {
	// One of ConfirmDestinationCommitted, ConfirmDestinationNotCommitted, ForceAbort.
	Action string `json:"action"`
	// One of DestinationCommitted, DestinationNotCommitted, Unknown --
	// must match Action (Confirm*Committed requires DestinationCommitted,
	// Confirm*NotCommitted requires DestinationNotCommitted; ForceAbort
	// accepts any).
	AcknowledgedDiagnosis string `json:"acknowledgedDiagnosis"`
	// Free-text evidence for what the operator observed. Required --
	// this is what turns a guess into an attested, audited decision.
	Reason string `json:"reason"`
}

const (
	RecoveryActionConfirmDestinationCommitted    = "ConfirmDestinationCommitted"
	RecoveryActionConfirmDestinationNotCommitted = "ConfirmDestinationNotCommitted"
	RecoveryActionForceAbort                     = "ForceAbort"

	RecoveryDiagnosisDestinationCommitted    = "DestinationCommitted"
	RecoveryDiagnosisDestinationNotCommitted = "DestinationNotCommitted"
	RecoveryDiagnosisUnknown                 = "Unknown"
)

type MachineMigrationStatus struct {
	Phase             string `json:"phase,omitempty"`
	Message           string `json:"message,omitempty"`
	SourceNode        string `json:"sourceNode,omitempty"`
	TargetNode        string `json:"targetNode,omitempty"`
	EffectiveStrategy string `json:"effectiveStrategy,omitempty"`
	RuntimeID         string `json:"runtimeID,omitempty"`
	SessionID         string `json:"sessionID,omitempty"`
	TransferID        string `json:"transferID,omitempty"`
	TransferPhase     string `json:"transferPhase,omitempty"`
	Backend           string `json:"backend,omitempty"`
	RAMTransferred    uint64 `json:"ramTransferred,omitempty"`
	RAMRemaining      uint64 `json:"ramRemaining,omitempty"`
	RAMTotal          uint64 `json:"ramTotal,omitempty"`
	TotalTimeMs       uint64 `json:"totalTimeMs,omitempty"`
	DowntimeMs        uint64 `json:"downtimeMs,omitempty"`
	// DataPlaneEncrypted reports whether the destination adapter had TLS
	// configured (-migration-data-tls) when this migration's receiver was
	// prepared -- the QEMU RAM/state stream itself, not the always-on mTLS
	// control-plane RPCs. Set once, at Starting, from the adapter's own
	// prepare() response (see internal/migration.PrepareResult); an older
	// adapter that predates this field simply omits it from JSON, which
	// decodes to false -- the safe, conservative assumption (unencrypted),
	// not fail-open.
	DataPlaneEncrypted bool `json:"dataPlaneEncrypted,omitempty"`
	// Recovery is populated only while Phase == NeedsRecovery: a live
	// diagnosis snapshot refreshed every reconcile tick, plus a permanent
	// record of whatever recovery action was actually applied (if any) --
	// kept even if spec.recovery is later edited or cleared.
	Recovery *MachineMigrationRecoveryStatus `json:"recovery,omitempty"`
}

// MachineMigrationRecoveryStatus mirrors migration.DiagnosisResult (the
// live ground truth Kairon can currently see) plus an audit trail of
// whatever recovery action was actually applied.
type MachineMigrationRecoveryStatus struct {
	SourceRuntimeStatus      string     `json:"sourceRuntimeStatus,omitempty"`
	DestinationSessionPhase  string     `json:"destinationSessionPhase,omitempty"`
	DestinationRuntimeStatus string     `json:"destinationRuntimeStatus,omitempty"`
	DestinationRuntimeFound  bool       `json:"destinationRuntimeFound,omitempty"`
	DiagnosedAt              *time.Time `json:"diagnosedAt,omitempty"`
	// AppliedAction/AppliedReason/AppliedAcknowledgedDiagnosis/AppliedAt
	// echo spec.recovery at the moment an action was actually taken --
	// a permanent audit record independent of later spec.recovery edits.
	AppliedAction                string     `json:"appliedAction,omitempty"`
	AppliedReason                string     `json:"appliedReason,omitempty"`
	AppliedAcknowledgedDiagnosis string     `json:"appliedAcknowledgedDiagnosis,omitempty"`
	AppliedAt                    *time.Time `json:"appliedAt,omitempty"`
}

func (m MachineMigration) Namespace() string {
	if m.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return m.Metadata.Namespace
}

type MachineSnapshot struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta            `json:"metadata"`
	Spec     MachineSnapshotSpec   `json:"spec"`
	Status   MachineSnapshotStatus `json:"status,omitempty"`
}

type MachineSnapshotList struct {
	TypeMeta `json:",inline"`
	Items    []MachineSnapshot `json:"items"`
}

type MachineSnapshotSpec struct {
	MachineName             string `json:"machineName"`
	VolumeSnapshotClassName string `json:"volumeSnapshotClassName,omitempty"`
}

type MachineSnapshotStatus struct {
	Phase           string                    `json:"phase,omitempty"`
	Message         string                    `json:"message,omitempty"`
	ReadyToUse      bool                      `json:"readyToUse,omitempty"`
	VolumeSnapshots []VolumeSnapshotReference `json:"volumeSnapshots,omitempty"`
}

type VolumeSnapshotReference struct {
	VolumeName         string `json:"volumeName"`
	VolumeSnapshotName string `json:"volumeSnapshotName"`
	ReadyToUse         bool   `json:"readyToUse,omitempty"`
}

func (s MachineSnapshot) Namespace() string {
	if s.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return s.Metadata.Namespace
}

// Minimal Kubernetes ResourceClaim representation used by the node agent
// and, for the topology hint in internal/controller's scheduling pass, by
// the controller too.
type ResourceClaim struct {
	Metadata ObjectMeta          `json:"metadata"`
	Status   ResourceClaimStatus `json:"status,omitempty"`
}

type ResourceClaimList struct {
	TypeMeta `json:",inline"`
	Items    []ResourceClaim `json:"items"`
}

type ResourceClaimStatus struct {
	Allocation *ResourceClaimAllocation `json:"allocation,omitempty"`
}

type ResourceClaimAllocation struct {
	Devices ResourceClaimDeviceAllocation `json:"devices,omitempty"`
}

type ResourceClaimDeviceAllocation struct {
	Results []DeviceRequestAllocationResult `json:"results,omitempty"`
}

type DeviceRequestAllocationResult struct {
	Request string `json:"request,omitempty"`
	Driver  string `json:"driver,omitempty"`
	Pool    string `json:"pool,omitempty"`
	Device  string `json:"device,omitempty"`
}

// ResourceSlice is a minimal resource.k8s.io/v1 ResourceSlice
// representation: enough to answer "which node hosts {Driver, Pool}",
// which is exactly what internal/controller needs to give the scheduler a
// DRA topology-awareness hint -- see internal/controller's
// draPreferredNode. Cluster-scoped, unlike ResourceClaim.
type ResourceSlice struct {
	Metadata ObjectMeta        `json:"metadata"`
	Spec     ResourceSliceSpec `json:"spec"`
}

type ResourceSliceSpec struct {
	Driver string                `json:"driver"`
	Pool   ResourceSlicePoolInfo `json:"pool"`
	// NodeName is empty for a pool not tied to a specific node (e.g. a
	// network-attached device pool) -- draPreferredNode contributes no
	// hint in that case, the same as if the claim isn't allocated yet.
	NodeName string `json:"nodeName,omitempty"`
}

type ResourceSlicePoolInfo struct {
	Name string `json:"name"`
}

type ResourceSliceList struct {
	TypeMeta `json:",inline"`
	Items    []ResourceSlice `json:"items"`
}

// Minimal CSI snapshot.storage.k8s.io/v1 representation.
type VolumeSnapshot struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta           `json:"metadata"`
	Spec     VolumeSnapshotSpec   `json:"spec"`
	Status   VolumeSnapshotStatus `json:"status,omitempty"`
}

type VolumeSnapshotSpec struct {
	Source                  VolumeSnapshotSource `json:"source"`
	VolumeSnapshotClassName *string              `json:"volumeSnapshotClassName,omitempty"`
}

type VolumeSnapshotSource struct {
	PersistentVolumeClaimName *string `json:"persistentVolumeClaimName,omitempty"`
}

type VolumeSnapshotStatus struct {
	ReadyToUse *bool                `json:"readyToUse,omitempty"`
	Error      *VolumeSnapshotError `json:"error,omitempty"`
	// RestoreSize is the minimum size a PVC restored from this snapshot
	// must request -- set by the real CSI driver, read by
	// MachineSnapshotRestore to size the new PVC when the request doesn't
	// override it explicitly.
	RestoreSize *string `json:"restoreSize,omitempty"`
}

type VolumeSnapshotError struct {
	Message *string `json:"message,omitempty"`
}

type Node struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Unschedulable bool `json:"unschedulable,omitempty"`
	} `json:"spec"`
	Status struct {
		Conditions []NodeCondition `json:"conditions,omitempty"`
		Addresses  []NodeAddress   `json:"addresses,omitempty"`
	} `json:"status"`
}

type NodeAddress struct {
	Type    string `json:"type"`
	Address string `json:"address"`
}

// SubjectAccessReview hand-rolls just the slice of the built-in
// authorization.k8s.io/v1 SubjectAccessReview wire format
// internal/uiapi's own console-access RBAC check needs (see
// internal/kube.Client.SubjectAccessReview) -- this project has no
// client-go/k8s.io/api dependency at all, the same reasoning
// internal/admission's own doc comment gives for hand-rolling
// AdmissionReview. Deliberately SubjectAccessReview, not
// SelfSubjectAccessReview: the caller (kairon-ui's own ServiceAccount
// token) and the subject being checked (an app-level kairon-ui operator
// identity, local account or OIDC claim) are different.
type SubjectAccessReview struct {
	TypeMeta `json:",inline"`
	Spec     SubjectAccessReviewSpec   `json:"spec"`
	Status   SubjectAccessReviewStatus `json:"status,omitempty"`
}

type SubjectAccessReviewSpec struct {
	User               string              `json:"user,omitempty"`
	Groups             []string            `json:"groups,omitempty"`
	ResourceAttributes *ResourceAttributes `json:"resourceAttributes,omitempty"`
}

type ResourceAttributes struct {
	Namespace   string `json:"namespace,omitempty"`
	Verb        string `json:"verb,omitempty"`
	Group       string `json:"group,omitempty"`
	Resource    string `json:"resource,omitempty"`
	Subresource string `json:"subresource,omitempty"`
	Name        string `json:"name,omitempty"`
}

type SubjectAccessReviewStatus struct {
	Allowed bool   `json:"allowed"`
	Denied  bool   `json:"denied,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type NodeList struct {
	Items []Node `json:"items"`
}

func (m Machine) Namespace() string {
	if m.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return m.Metadata.Namespace
}

func (m Machine) RuntimeName() string {
	return "kairon-" + m.Namespace() + "-" + m.Metadata.Name
}

func (m Machine) DesiredPowerState() string {
	if m.Spec.PowerState == "" {
		return "Running"
	}
	return m.Spec.PowerState
}

func HasFinalizer(m Machine, name string) bool {
	return HasFinalizerList(m.Metadata.Finalizers, name)
}

func HasFinalizerList(finalizers []string, name string) bool {
	for _, f := range finalizers {
		if f == name {
			return true
		}
	}
	return false
}

func RemoveFinalizer(finalizers []string, name string) []string {
	out := make([]string, 0, len(finalizers))
	for _, f := range finalizers {
		if f != name {
			out = append(out, f)
		}
	}
	return out
}

func AnnotationTrue(meta ObjectMeta, key string) bool {
	return meta.Annotations != nil && meta.Annotations[key] == "true"
}
