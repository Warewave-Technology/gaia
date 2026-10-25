// The API's shapes, as internal/server and internal/auth write them.

export type Role = "viewer" | "operator" | "platform-admin";

export type Me = {
  id: string;
  username: string;
  session: string;
  sessionKind: "ui" | "cli-access";
  teams: string[];
  roles: Role[];
  strongAuthAt?: string;
};

export type RunStatus = "queued" | "running" | "succeeded" | "failed" | "cancelled";

export type Stage = {
  contract: string;
  stage: string;
  status: "running" | "succeeded" | "failed" | "cancelled";
  detail?: string;
  startedAt: string;
  finishedAt?: string;
};

export type Run = {
  id: string;
  contracts: string[];
  commit: string;
  planHash: string;
  status: RunStatus;
  error?: string;
  requestedBy?: string;
  queuedAt: string;
  startedAt?: string;
  finishedAt?: string;
  configCommit?: string;
  eeImage?: string;
  stages?: Stage[];
  record?: { seq: number; recordHash: string; prevHash: string; archived?: string };
  change?: string;
  hosts?: Record<string, string>;
  restricted?: boolean;
};

export type RunEvent = {
  seq: number;
  stage: string;
  type: string;
  host?: string;
  task?: string;
  changed?: boolean;
  failed?: boolean;
  stdout?: string;
  data?: Record<string, unknown>;
  createdAt: string;
};

export type ChangeStatus = "pending" | "approved" | "running" | "applied" | "failed" | "cancelled" | "rejected" | "superseded" | "expired";

export type Commit = { repo: string; commit: string; signer?: string; for?: string; reverts?: string; problem?: string };

export type ApprovalGroup = { name: string; teams: string[]; role: string; candidates: string[]; approvedBy?: string; why: string };

export type Gate = { gate: string; status: "ok" | "waiting" | "failed"; detail?: unknown };

export type Approval = { username: string; decision: "approve" | "reject"; planHash: string; comment?: string; createdAt: string };

export type Change = {
  id: string;
  contract: string;
  source: string;
  status: ChangeStatus;
  label?: string;
  planHash?: string;
  requestedBy?: string[];
  commits?: Commit[];
  gates?: Gate[];
  approvals?: Approval[];
  plan?: unknown;
  snapshot?: unknown;
  runId?: string;
  createdAt: string;
  updatedAt: string;
  finishedAt?: string;
  restricted?: boolean;
  awaitingMe?: boolean;
};

export type ContractSummary = {
  name: string;
  liveHosts: number;
  lastRun?: string;
  lastStatus?: RunStatus;
  sync?: "synced" | "out-of-sync" | "unknown";
  openChange?: string;
  restricted?: boolean;
};
