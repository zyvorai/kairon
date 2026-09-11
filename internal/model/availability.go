package model

const (
	KindMachineDisruptionBudget = "MachineDisruptionBudget"
	KindMachineMigration        = "MachineMigration"

	AnnotationEvacuate   = "kairon.zyvor.dev/evacuate"
	AnnotationFenceSince = "kairon.zyvor.dev/fence-since"

	ConditionNodeHealthy = "NodeHealthy"
)

// MachineDisruptionBudget limits voluntary disruptions (evacuation) like a PDB.
type MachineDisruptionBudget struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta                    `json:"metadata"`
	Spec     MachineDisruptionBudgetSpec   `json:"spec"`
	Status   MachineDisruptionBudgetStatus `json:"status,omitempty"`
}

type MachineDisruptionBudgetList struct {
	TypeMeta `json:",inline"`
	Items    []MachineDisruptionBudget `json:"items"`
}

type MachineDisruptionBudgetSpec struct {
	Selector       map[string]string `json:"selector,omitempty"`
	MaxUnavailable int               `json:"maxUnavailable"`
}

type MachineDisruptionBudgetStatus struct {
	CurrentHealthy     int   `json:"currentHealthy"`
	DesiredHealthy     int   `json:"desiredHealthy"`
	DisruptionsAllowed int   `json:"disruptionsAllowed"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

func (m MachineDisruptionBudget) Namespace() string {
	if m.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return m.Metadata.Namespace
}

// MachineMigration drives FluxVM live migration for a Machine on its source node.
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
	MachineName    string `json:"machineName"`
	Destination    string `json:"destination"`
	Mode           string `json:"mode,omitempty"`
	BandwidthMbps  int64  `json:"bandwidthMbps,omitempty"`
	MaxDowntimeMs  int64  `json:"maxDowntimeMs,omitempty"`
	TargetNodeName string `json:"targetNodeName,omitempty"`
}

type MachineMigrationStatus struct {
	Phase              string `json:"phase,omitempty"`
	Message            string `json:"message,omitempty"`
	SourceNode         string `json:"sourceNode,omitempty"`
	RuntimeID          string `json:"runtimeID,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

func (m MachineMigration) Namespace() string {
	if m.Metadata.Namespace == "" {
		return DefaultNamespace
	}
	return m.Metadata.Namespace
}

func LabelsMatch(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return true
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
