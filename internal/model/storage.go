package model

const (
	KindMachineImage    = "MachineImage"
	KindVirtualDisk     = "VirtualDisk"
	KindMachineSnapshot = "MachineSnapshot"
)

// MachineImage is a reusable VM image reference (host path or OCI digest declaration).
type MachineImage struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta         `json:"metadata"`
	Spec     MachineImageSpec   `json:"spec"`
	Status   MachineImageStatus `json:"status,omitempty"`
}

type MachineImageList struct {
	TypeMeta `json:",inline"`
	Items    []MachineImage `json:"items"`
}

type MachineImageSpec struct {
	Source MachineImageSource `json:"source"`
	Digest string             `json:"digest,omitempty"`
}

type MachineImageSource struct {
	Path string `json:"path,omitempty"`
	OCI  string `json:"oci,omitempty"`
}

type MachineImageStatus struct {
	Phase              string `json:"phase,omitempty"`
	ResolvedPath       string `json:"resolvedPath,omitempty"`
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

func (m MachineImage) Namespace() string {
	if m.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return m.Metadata.Namespace
}

// VirtualDisk binds a FluxVM-backed disk to a host path or PVC.
type VirtualDisk struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta        `json:"metadata"`
	Spec     VirtualDiskSpec   `json:"spec"`
	Status   VirtualDiskStatus `json:"status,omitempty"`
}

type VirtualDiskList struct {
	TypeMeta `json:",inline"`
	Items    []VirtualDisk `json:"items"`
}

type VirtualDiskSpec struct {
	Size              string            `json:"size,omitempty"`
	Storage           string            `json:"storage,omitempty"`
	Source            VirtualDiskSource `json:"source"`
	AccessMode        string            `json:"accessMode,omitempty"`
	CloneFromDisk     string            `json:"cloneFromDisk,omitempty"`
	CloneFromSnapshot string            `json:"cloneFromSnapshot,omitempty"`
}

type VirtualDiskSource struct {
	HostPath              string  `json:"hostPath,omitempty"`
	PersistentVolumeClaim *PVCRef `json:"persistentVolumeClaim,omitempty"`
}

type PVCRef struct {
	ClaimName string `json:"claimName"`
}

type VirtualDiskStatus struct {
	Phase              string `json:"phase,omitempty"`
	Path               string `json:"path,omitempty"`
	VolumeName         string `json:"volumeName,omitempty"`
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

func (d VirtualDisk) Namespace() string {
	if d.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return d.Metadata.Namespace
}

// MachineSnapshot captures a FluxVM snapshot tag for a Machine on its assigned node.
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
	MachineName string `json:"machineName"`
	Tag         string `json:"tag"`
}

type MachineSnapshotStatus struct {
	Phase              string `json:"phase,omitempty"`
	Tag                string `json:"tag,omitempty"`
	NodeName           string `json:"nodeName,omitempty"`
	RuntimeID          string `json:"runtimeID,omitempty"`
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

func (s MachineSnapshot) Namespace() string {
	if s.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return s.Metadata.Namespace
}

type SharedFolder struct {
	HostPath  string `json:"hostPath"`
	GuestPath string `json:"guestPath"`
	ReadOnly  bool   `json:"readOnly,omitempty"`
}

type CloudInitFile struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	Permissions string `json:"permissions,omitempty"`
}

// PersistentVolumeClaim / PersistentVolume subsets for PVC→hostPath resolution.
type PersistentVolumeClaim struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		VolumeName string `json:"volumeName,omitempty"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase,omitempty"`
	} `json:"status"`
}

type PersistentVolume struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		HostPath *struct {
			Path string `json:"path"`
		} `json:"hostPath,omitempty"`
		Local *struct {
			Path string `json:"path"`
		} `json:"local,omitempty"`
		CSI *struct {
			VolumeHandle string `json:"volumeHandle,omitempty"`
		} `json:"csi,omitempty"`
	} `json:"spec"`
}
