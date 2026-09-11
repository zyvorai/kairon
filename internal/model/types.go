package model

import "time"

const (
	APIVersion       = "kairon.zyvor.dev/v1alpha1"
	KindMachine      = "Machine"
	Finalizer        = "kairon.zyvor.dev/runtime-cleanup"
	CapableLabel     = "kairon.zyvor.dev/capable"
	DefaultNamespace = "default"
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
	NodeName   string        `json:"nodeName,omitempty"`
	Image      ImageSpec     `json:"image"`
	Resources  ResourceSpec  `json:"resources"`
	Runtime    RuntimeSpec   `json:"runtime,omitempty"`
	Network    NetworkSpec   `json:"network,omitempty"`
	PowerState string        `json:"powerState,omitempty"`
	Tenant     string        `json:"tenant,omitempty"`
	TTLSeconds int64         `json:"ttlSeconds,omitempty"`
	Placement  PlacementSpec `json:"placement,omitempty"`
	Security   SecuritySpec  `json:"security,omitempty"`
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

type NetworkSpec struct {
	Mode   string `json:"mode,omitempty"`
	NetNS  bool   `json:"netns,omitempty"`
	Bridge string `json:"bridge,omitempty"`
	Parent string `json:"parent,omitempty"`
	MAC    string `json:"mac,omitempty"`
}

type PlacementSpec struct {
	Architecture string            `json:"architecture,omitempty"`
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

type SecuritySpec struct {
	SecureBoot bool `json:"secureBoot,omitempty"`
	TPM        bool `json:"tpm,omitempty"`
}

type MachineStatus struct {
	Phase              string      `json:"phase,omitempty"`
	NodeName           string      `json:"nodeName,omitempty"`
	RuntimeID          string      `json:"runtimeID,omitempty"`
	GuestIP            string      `json:"guestIP,omitempty"`
	ObservedGeneration int64       `json:"observedGeneration,omitempty"`
	Message            string      `json:"message,omitempty"`
	Conditions         []Condition `json:"conditions,omitempty"`
}

type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

type Node struct {
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Unschedulable bool `json:"unschedulable,omitempty"`
	} `json:"spec"`
	Status struct {
		Conditions []NodeCondition `json:"conditions,omitempty"`
	} `json:"status"`
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
	for _, f := range m.Metadata.Finalizers {
		if f == name {
			return true
		}
	}
	return false
}
