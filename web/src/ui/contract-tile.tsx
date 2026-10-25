// A contract as a card, like Argo CD's application tiles.
import { Link } from "react-router-dom";
import type { ContractSummary } from "../api/types";
import { health, syncState } from "../lib/contract";
import { ago } from "../lib/time";
import { Icon } from "./icons";
import { Hint, Mono, shortLabel } from "./parts";
import { HEALTH, HealthLabel, Sync } from "./status";

export function ContractTile({ c, drift }: { c: ContractSummary; drift: number }) {
  if (c.restricted) {
    return (
      <div className="card" style={{ padding: "14px 16px", borderLeft: "4px solid var(--bg3)" }}>
        <span className="il" style={{ color: "var(--fg4)" }}><Icon name="lock" size={16} />{c.name}</span>
        <div className="faint" style={{ marginTop: 8, fontSize: 13 }}>Bu kontratın sadece adını görebilirsiniz.</div>
      </div>
    );
  }
  const h = health(c.lastStatus, c.liveHosts, drift);
  const s = syncState(c.sync, drift);
  const l = c.labels ?? {};
  const dt = { color: "var(--gray)", display: "flex", alignItems: "center", gap: 4 } as const;
  return (
    <div className="card" style={{ borderLeft: `4px solid ${HEALTH[h].color}`, display: "flex", flexDirection: "column" }}>
      <div className="spread" style={{ padding: "14px 16px 0" }}>
        <Link to={`/contracts/${encodeURIComponent(c.name)}`} className="il" style={{ fontSize: 15, fontWeight: 700, color: "var(--fg)" }}>
          <span style={{ color: "var(--yellow)" }}><Icon name="file" size={16} /></span>{c.name}
        </Link>
        <span className="row" style={{ gap: 12, fontSize: 13 }}><HealthLabel h={h} /><Sync state={s} /></span>
      </div>
      <dl style={{ margin: 0, padding: "12px 16px 14px", display: "grid", gridTemplateColumns: "112px minmax(0, 1fr)", gap: "5px 12px", fontSize: 13 }}>
        <dt style={dt}>Sahip</dt><dd style={{ margin: 0 }}>{l.owner ?? "–"}</dd>
        <dt style={dt}>Ortam</dt><dd style={{ margin: 0 }}>{[l.env, l.app && `app ${l.app}`].filter(Boolean).join(" · ") || "–"}</dd>
        <dt style={dt}>Uygulanan<Hint>Gerçekte uygulanmış olan: son başarılı run'ın commit'i.</Hint></dt>
        <dd style={{ margin: 0 }}><Mono>{shortLabel(c.appliedLabel) || "–"}</Mono></dd>
        <dt style={dt}>Git'te<Hint>Uygulanandan farklıysa onay ya da uygulama bekleyen bir Change vardır.</Hint></dt>
        <dd style={{ margin: 0 }}>{c.openChange ? <Link to={`/changes/${encodeURIComponent(c.openChange)}`} style={{ color: "var(--blue)" }}>Change açık</Link> : <span className="muted">uygulananla aynı</span>}</dd>
        <dt style={dt}>Sunucu</dt><dd style={{ margin: 0 }}>{c.liveHosts}</dd>
        <dt style={dt}>Son uygulama</dt><dd style={{ margin: 0 }}>{c.appliedAt ? ago(c.appliedAt) : "–"}</dd>
      </dl>
    </div>
  );
}
