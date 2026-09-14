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
	NodeName      string                 `json:"nodeName,omitempty"`
	Image         ImageSpec              `json:"image"`
	Resources     ResourceSpec           `json:"resources"`
	Runtime       RuntimeSpec            `json:"runtime,omitempty"`
	Network       NetworkSpec            `json:"network,omitempty"`
	CloudInit     CloudInitSpec          `json:"cloudInit,omitempty"`
	ServiceFabric ServiceFabricSpec      `json:"serviceFabric,omitempty"`
	PowerState    string                 `json:"powerState,omitempty"`
	Tenant        string                 `json:"tenant,omitempty"`
	TTLSeconds    int64                  `json:"ttlSeconds,omitempty"`
	Placement     PlacementSpec          `json:"placement,omitempty"`
	Security      SecuritySpec           `json:"security,omitempty"`
	Volumes       []MachineVolume        `json:"volumes,omitempty"`
	DeviceClaims  []DeviceClaimReference `json:"deviceClaims,omitempty"`
	GuestAgent    GuestAgentSpec         `json:"guestAgent,omitempty"`
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
	Path   string `json:"path"`
	Digest string `json:"digest,omitempty"`
}

type ResourceSpec struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

type RuntimeSpec struct {
	Backend string `json:"backend,omitempty"`
	Kernel  string `json:"kernel,omitempty"`
}

type PlacementSpec struct {
	Architecture string            `json:"architecture,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Affinity/AntiAffinity are required (hard) constraints only -- filters
	// consulted by internal/scheduler.Scheduler.eligible against every other
	// currently-scheduled Machine, not a weighted scoring pass. Preferred
	// (soft) affinity and topology spread constraints need a real scoring
	// system Kairon's "least-loaded, deterministic tie-break" scheduler
	// doesn't have yet -- deliberately out of scope for this pass.
	Affinity     []MachineAffinityTerm `json:"affinity,omitempty"`
	AntiAffinity []MachineAffinityTerm `json:"antiAffinity,omitempty"`
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

// Minimal Kubernetes ResourceClaim representation used by the node agent.
type ResourceClaim struct {
	Metadata ObjectMeta          `json:"metadata"`
	Status   ResourceClaimStatus `json:"status,omitempty"`
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
