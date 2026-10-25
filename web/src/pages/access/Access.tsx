// Access: who may log in to which servers (design 14.3): the access
// groups, the teams they are made of, and each contract's access entries.
import { useQuery } from "@tanstack/react-query";
import { Link, Navigate, useParams } from "react-router-dom";
import { api } from "../../api/client";
import { field, str, strs, useObjects, type CatalogObj } from "../../api/objects";
import { Icon } from "../../ui/icons";
import { Badge, Card, ErrorNote, Hint, Leg, Loading, Mono } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { Tabs } from "../../ui/tabs";
import { dash, ObjectTable, ObjectsError, Ref, type Col } from "../../ui/objects";
import { AddButton } from "../../ui/settings-editor";
import { hasRole, useSession } from "../../api/session";
import { useUsers } from "../settings/Users";

type Entry = { contract: string; group?: string; team?: string; sudo?: boolean; commands?: string[] };

function Refs({ kind, names }: { kind: string; names: string[] }) {
  if (names.length === 0) return dash;
  return <span className="row" style={{ gap: 10 }}>{names.map((n) => <Ref key={n} kind={kind} name={n} />)}</span>;
}

function dn(o: CatalogObj, path: string) {
  const v = str(field(o.spec, path));
  return v ? <Mono>{v}</Mono> : dash;
}

const GROUP_COLS: Col[] = [
  { title: "Description", cell: (o) => str(field(o.spec, "description")) || dash },
  { title: "Teams", cell: (o) => <Refs kind="Team" names={strs(field(o.spec, "teams"))} /> },
  { title: "Groups", hint: "Other access groups it includes.", cell: (o) => <Refs kind="Group" names={strs(field(o.spec, "groups"))} /> },
  { title: "Owner", hint: "The team that approves changes to its members.", cell: (o) => <Ref kind="Team" name={str(field(o.spec, "owner"))} /> },
  { title: "Privileged", hint: "Sudo through a privileged group needs a platform admin's approval.", cell: (o) => (field(o.spec, "privileged") === true ? <Badge tone="orange" icon="shield">privileged</Badge> : dash) },
];

function teamCols(members: Map<string, string[]> | null): Col[] {
  const cols = [...TEAM_COLS];
  if (members) {
    cols.splice(1, 0, {
      title: "Members", hint: "The team's members in Gaia: synced from the directory, or set by hand.",
      cell: (o) => {
        const xs = members.get(o.name) ?? [];
        if (xs.length === 0) return dash;
        return <span className="row" style={{ gap: 4 }} title={xs.join(", ")}>{xs.slice(0, 4).map((x) => <span key={x} className="badge gray" style={{ fontWeight: 500 }}><Icon name="user" size={12} />{x}</span>)}{xs.length > 4 && <span className="faint">+{xs.length - 4}</span>}</span>;
      },
    });
  }
  return cols;
}

const TEAM_COLS: Col[] = [
  { title: "Display name", cell: (o) => str(field(o.spec, "displayName")) || dash },
  { title: "Directory group", hint: "The directory group its members come from.", cell: (o) => dn(o, "adGroup.dn") },
  { title: "Approvers", hint: "The directory group of whoever approves the team's changes.", cell: (o) => dn(o, "approversGroup.dn") },
  { title: "Secrets prefix", hint: "The OpenBao path the team's contracts may refer to.", cell: (o) => dn(o, "secrets.prefix") },
];

function SudoCell({ e }: { e: Entry }) {
  if (e.sudo) return <Badge tone="orange" icon="key">full sudo</Badge>;
  if (e.commands?.length) {
    return (
      <span style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        {e.commands.map((c) => <span key={c} className="il" style={{ fontWeight: 400 }}><span className="faint"><Icon name="terminal" size={13} /></span><Mono>{c}</Mono></span>)}
      </span>
    );
  }
  return <span className="faint">no sudo</span>;
}

function ContractAccess() {
  const q = useQuery({ queryKey: ["access"], queryFn: () => api<Entry[]>("/access") });
  return (
    <Card title="Contract access" icon="file" hint={<>Who may log in to each contract's servers, and what they may run as root. Read from the contract's <b className="term">access</b> field.
      <Leg icon="key" color="var(--orange)" name="full sudo">may run any command as root</Leg>
      <Leg icon="terminal" color="var(--fg4)" name="command">may run only the listed commands as root</Leg>
      <Leg icon="minus" color="var(--fg4)" name="no sudo">may log in, not become root</Leg></>}
      right={<span className="faint">{q.data?.length ?? 0} entries</span>}>
      {q.isPending ? <Loading /> : q.error ? <ObjectsError error={q.error} /> : q.data.length === 0 ? <div className="empty">No contract gives access.</div> : (
        <table className="grid">
          <thead><tr><th>Contract</th><th>Who</th><th>Sudo</th></tr></thead>
          <tbody>
            {q.data.map((e, i) => {
              const first = i === 0 || q.data[i - 1]!.contract !== e.contract;
              return (
                <tr key={i} style={{ verticalAlign: "top" }}>
                  <td style={first ? undefined : { borderTopColor: "transparent" }}>{first && <Link to={`/contracts/${encodeURIComponent(e.contract)}`} className="il"><Icon name="file" size={14} />{e.contract}</Link>}</td>
                  <td>{e.group ? <Ref kind="Group" name={e.group} /> : <Ref kind="Team" name={e.team ?? ""} />}</td>
                  <td><SudoCell e={e} /></td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </Card>
  );
}

const TABS = [
  { tab: "contracts", label: "Contract access", icon: "file" as const },
  { tab: "Group", label: "Groups", icon: "shield" as const },
  { tab: "Team", label: "Teams", icon: "users" as const },
];

export function Access() {
  const { tab = "" } = useParams();
  const q = useObjects(["Group", "Team"]);
  const admin = hasRole(useSession(), "platform-admin");
  const users = useUsers(admin && tab === "Team");
  let members: Map<string, string[]> | null = null;
  if (users.data) {
    members = new Map();
    for (const u of users.data) for (const t of u.teams) members.set(t, [...(members.get(t) ?? []), u.username]);
  }
  const t = TABS.find((x) => x.tab === tab);
  if (!t) return <Navigate to="/access/contracts" replace />;
  const objs = (k: string) => (q.data?.objects ?? []).filter((o) => o.kind === k);
  return (
    <Page crumbs={<><Link to="/access">Access</Link> / {t.label}</>}>
      <div className="title-row">
        <h1>Access</h1>
        <Hint width={340}>Who may log in to servers. A contract gives access to a group or a team; a group is teams together; team members come from the directory.</Hint>
      </div>
      <Tabs label="Access" items={TABS.map((x) => ({ to: `/access/${x.tab}`, icon: x.icon, label: x.label, count: x.tab === "contracts" ? undefined : q.data ? objs(x.tab).length : undefined }))} />
      {tab === "contracts" ? <ContractAccess />
        : q.isPending ? <Loading />
        : q.error ? <ErrorNote error={q.error} />
        : tab === "Group"
          ? <ObjectTable kind="Group" title="Groups" hint="Access groups: teams (and other groups) together. Contracts give access to them." cols={GROUP_COLS} objs={objs("Group")} right={<AddButton kind="Group" label="Add Group" />} />
          : <ObjectTable kind="Team" title="Teams" hint="Teams and their approvers. With a TeamSource they are made from directory groups." cols={teamCols(members)} objs={objs("Team")} right={<AddButton kind="Team" label="Add Team" />} />}
    </Page>
  );
}
