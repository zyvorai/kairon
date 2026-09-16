// Mirrors internal/model/types.go's JSON shapes, trimmed to the fields
// the UI actually renders or submits. Kept as one shared file since every
// page depends on the same handful of resource shapes.

export interface ObjectMeta {
  name: string;
  namespace?: string;
  annotations?: Record<string, string>;
}

export interface Machine {
  metadata: ObjectMeta;
  spec: {
    nodeName?: string;
    image: { path: string };
    resources: { cpu: string; memory: string };
    runtime?: { backend?: string };
    network?: { mode?: string; netns?: boolean };
    powerState?: string;
    guestAgent?: { enabled?: boolean; console?: boolean };
  };
  status?: {
    phase?: string;
    nodeName?: string;
    guestIP?: string;
    message?: string;
    resourceUsage?: { cpuPercent?: number; memoryBytes?: number; diskReadBytes?: number; diskWriteBytes?: number };
  };
}

export interface RecoveryStatus {
  sourceRuntimeStatus?: string;
  destinationSessionPhase?: string;
  destinationRuntimeStatus?: string;
  destinationRuntimeFound?: boolean;
  diagnosedAt?: string;
  appliedAction?: string;
  appliedReason?: string;
  appliedAcknowledgedDiagnosis?: string;
  appliedAt?: string;
}

export interface MachineMigration {
  metadata: ObjectMeta;
  spec: {
    machineName: string;
    strategy?: string;
    targetNode?: string;
    mode?: string;
    bandwidthMbps?: number;
    maxDowntimeMs?: number;
    multifdChannels?: number;
    migrationNetwork?: string;
  };
  status?: {
    phase?: string;
    message?: string;
    sourceNode?: string;
    targetNode?: string;
    effectiveStrategy?: string;
    transferPhase?: string;
    backend?: string;
    ramTransferred?: number;
    ramRemaining?: number;
    ramTotal?: number;
    totalTimeMs?: number;
    downtimeMs?: number;
    dataPlaneEncrypted?: boolean;
    recovery?: RecoveryStatus;
  };
}

export interface MachineSnapshot {
  metadata: ObjectMeta;
  spec: { machineName: string; volumeSnapshotClassName?: string };
  status?: { phase?: string; readyToUse?: boolean; message?: string };
}

export interface KaironNode {
  metadata: ObjectMeta;
  status?: { addresses?: { type: string; address: string }[] };
}

export interface Overview {
  machines: { total: number; byPhase: Record<string, number> };
  migrations: { total: number; active: number; needsRecovery: number; byPhase: Record<string, number> };
  nodes: number;
}

// The five resource kinds below are read-only, list-only in the dashboard
// today (see internal/uiapi/fleet.go) -- kaironctl/kubectl remain the way
// to create or mutate any of them.

export interface MachineQuota {
  metadata: ObjectMeta;
  spec: { maxMachines?: number; maxTotalCpu?: string; maxTotalMemory?: string };
  status?: { usedMachines?: number; usedTotalCpuCores?: number; usedTotalMemoryMiB?: number };
}

export interface MachineDisruptionBudget {
  metadata: ObjectMeta;
  spec: { selector: Record<string, string>; minAvailable?: string; maxUnavailable?: string };
  status?: { expectedMachines: number; currentHealthy: number; desiredHealthy: number; disruptionsAllowed: number };
}

export interface MachineSet {
  metadata: ObjectMeta;
  spec: { replicas: number; strategy?: string; maxUnavailable?: string };
  status?: { replicas?: number; readyReplicas?: number; updatedReplicas?: number; message?: string };
}

export interface MachineInstanceType {
  metadata: ObjectMeta;
  spec: {
    resources: {
      cpu: string;
      memory: string;
      maxCpu?: string;
      maxMemory?: string;
      hugepages?: boolean;
      numaNode?: number;
      cpuSet?: string;
      cpuPinning?: boolean;
    };
  };
}

export interface MigrationPolicy {
  metadata: ObjectMeta;
  spec: { selector: Record<string, string>; bandwidthMbps?: number; maxConcurrent?: number };
  status?: { activeMigrations?: number };
}

export interface MachineSnapshotSchedule {
  metadata: ObjectMeta;
  spec: {
    selector: Record<string, string>;
    intervalSeconds: number;
    volumeSnapshotClassName?: string;
    suspend?: boolean;
    keepLast?: number;
  };
  status?: { lastRunTime?: string; lastRunSnapshotCount?: number; lastRunError?: string };
}
