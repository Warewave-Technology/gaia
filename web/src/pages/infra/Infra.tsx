// Altyapı: where servers go and what they are made of (design 17.8):
// sites, networks, images and sizes, one tab each.
import { type ReactNode } from "react";
import { Link, Navigate, useParams } from "react-router-dom";
import { field, str, strs, useObjects, type CatalogObj } from "../../api/objects";
import { Hint, Loading, Mono } from "../../ui/parts";
import { Page } from "../../ui/shell";
import { Tabs } from "../../ui/tabs";
import { kindOf } from "../../ui/kinds";
import { dash, List, ObjectTable, ObjectsError, Ref, type Col } from "../../ui/objects";

function attachment(o: CatalogObj): ReactNode {
  const a = (field(o.spec, "attachment") ?? {}) as Record<string, unknown>;
  if (a.portgroup) return <span><span className="faint">portgroup </span><Mono>{str(a.portgroup)}</Mono></span>;
  if (a.vnet) return <span><span className="faint">vnet </span><Mono>{str(a.vnet)}</Mono></span>;
  if (a.bridge) return <span><span className="faint">bridge </span><Mono>{str(a.bridge)}</Mono>{a.vlan ? <span className="faint"> · vlan {str(a.vlan)}</span> : null}</span>;
  return dash;
}

function placement(o: CatalogObj): string {
  const c = (field(o.spec, "compute") ?? {}) as Record<string, unknown>;
  const parts = [c.datacenter, c.cluster, strs(c.nodes).join(", "), c.pool, field(c as Record<string, unknown>, "storage.default")].map(str).filter(Boolean);
  return parts.join(" · ");
}

function templates(o: CatalogObj): ReactNode {
  const ts = (field(o.spec, "templates") ?? []) as Record<string, unknown>[];
  if (!Array.isArray(ts) || ts.length === 0) return dash;
  return (
    <span style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      {ts.map((t, i) => {
        const ref = t.template;
        const name = typeof ref === "object" && ref !== null ? (str((ref as Record<string, unknown>).name) || `vmid ${str((ref as Record<string, unknown>).vmid)}`) : str(ref);
        return <span key={i}><Ref kind="Provider" name={str(t.provider)} /> <Mono>{name}</Mono>{t.customization ? <span className="faint"> · {str(t.customization)}</span> : null}</span>;
      })}
    </span>
  );
}

const TABS: { kind: string; label: string; hint: string; cols: Col[]; sort?: (a: CatalogObj, b: CatalogObj) => number }[] = [
  {
    kind: "Site", label: "Site'lar", hint: "Veri merkezleri. Her site'ın VM'leri hangi compute provider'ında, hangi küme ve depolamada açacağını söyler.",
    cols: [
      { title: "Compute", hint: "VM'lerin açıldığı provider.", cell: (o) => <Ref kind="Provider" name={str(field(o.spec, "compute.provider"))} /> },
      { title: "Yerleşim", hint: "Datacenter, küme, node'lar, havuz ve varsayılan depolama; provider'ın türüne göre.", cell: (o) => placement(o) || dash },
      { title: "NTP", cell: (o) => <List xs={strs(field(o.spec, "ntp"))} mono /> },
      { title: "Zabbix proxy", cell: (o) => str(field(o.spec, "zabbixProxy")) || dash },
    ],
  },
  {
    kind: "Network", label: "Ağlar", hint: "Sunucuların bağlandığı ağlar: hangi site'ta, VM'e nasıl bağlandığı ve adreslerin hangi IPAM havuzundan geldiği.",
    cols: [
      { title: "Site", cell: (o) => <Ref kind="Site" name={str(field(o.spec, "site"))} /> },
      { title: "Bağlantı", hint: "VM'in ağa nasıl bağlandığı: vSphere portgroup, Proxmox bridge (VLAN ile) ya da SDN VNet.", cell: attachment },
      { title: "IPAM havuzu", cell: (o) => <span><Mono>{str(field(o.spec, "ipam.pool"))}</Mono> <span className="faint">· {str(field(o.spec, "ipam.provider"))}</span></span>, nowrap: true },
      { title: "Ağ geçidi", cell: (o) => str(field(o.spec, "gateway")) ? <Mono>{str(field(o.spec, "gateway"))}</Mono> : dash },
      { title: "Ortamlar", hint: "Bu ağı kullanabilecek ortamlar (env etiketi); boşsa hepsi.", cell: (o) => strs(field(o.spec, "allowedEnvironments")).length ? <List xs={strs(field(o.spec, "allowedEnvironments"))} /> : <span className="muted">hepsi</span> },
    ],
  },
  {
    kind: "Image", label: "İmajlar", hint: "VM'lerin klonlandığı şablonlar: işletim sistemi, Gaia'nın bağlanma yolu ve her provider'daki şablon.",
    cols: [
      { title: "İşletim sistemi", cell: (o) => str(field(o.spec, "os")) || dash },
      { title: "Bağlantı", hint: "Gaia'nın sunucuya bağlandığı yol: ssh ya da winrm.", cell: (o) => str(field(o.spec, "connection")) || dash },
      { title: "Şablonlar", hint: "Her compute provider'ındaki şablon ve ilk açılış yöntemi.", cell: templates },
    ],
  },
  {
    kind: "Size", label: "Boyutlar", hint: "VM boyutları: vCPU, bellek ve disk. Kontrat boyutu adıyla ister.",
    cols: [
      { title: "vCPU", cell: (o) => str(field(o.spec, "cpu")) },
      { title: "Bellek", cell: (o) => `${str(field(o.spec, "memoryGiB"))} GiB` },
      { title: "Disk", cell: (o) => `${str(field(o.spec, "diskGiB"))} GiB` },
    ],
    sort: (a, b) => Number(field(a.spec, "cpu")) - Number(field(b.spec, "cpu")) || Number(field(a.spec, "memoryGiB")) - Number(field(b.spec, "memoryGiB")),
  },
];
const KINDS = TABS.map((t) => t.kind);

export function Infra() {
  const { kind = "" } = useParams();
  const q = useObjects(KINDS);
  const tab = TABS.find((t) => t.kind === kind);
  if (!tab) return <Navigate to="/infra/Site" replace />;
  const objs = (q.data?.objects ?? []).filter((o) => o.kind === kind).sort(tab.sort ?? ((a, b) => a.name.localeCompare(b.name)));
  const count = (k: string) => q.data?.objects.filter((o) => o.kind === k).length;
  return (
    <Page crumbs={<><Link to="/infra">Altyapı</Link> / {tab.label}</>}>
      <div className="title-row">
        <h1>Altyapı</h1>
        <Hint>Sunucuların nereye ve neyle açılacağı: site, ağ, imaj ve boyut. Kontratlar bunlara adıyla referans verir.</Hint>
      </div>
      <Tabs label="Altyapı" items={TABS.map((t) => ({ to: `/infra/${t.kind}`, icon: kindOf(t.kind).icon, label: t.label, count: count(t.kind) }))} />
      {q.isPending ? <Loading /> : q.error ? <ObjectsError error={q.error} /> : (
        <ObjectTable kind={kind} title={tab.label} hint={tab.hint} cols={tab.cols} objs={objs} />
      )}
    </Page>
  );
}

