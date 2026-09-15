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
