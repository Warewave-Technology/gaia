// A tab bar under a screen's title: each tab is a link, so a tab can be
// opened, shared and reached with back.
import type { ReactNode } from "react";
import { NavLink } from "react-router-dom";
import { Icon, type IconName } from "./icons";
import { Hint } from "./parts";

export type TabItem = { to: string; icon: IconName; label: string; count?: number; title?: string };

export function Tabs({ items, label }: { items: TabItem[]; label: string }) {
  return (
    <nav className="tabs" aria-label={label}>
      {items.map((t) => (
        <NavLink key={t.to} to={t.to} title={t.title} className={({ isActive }) => (isActive ? "active" : "")}>
          <Icon name={t.icon} size={15} />
          <span>{t.label}</span>
          {t.count !== undefined && <span className="tab-count">{t.count}</span>}
        </NavLink>
      ))}
    </nav>
  );
}

// TabHead opens a tab: what it is, its "?" and the tab's actions on the
// right.
export function TabHead({ icon, text, hint, right }: { icon: IconName; text: ReactNode; hint?: ReactNode; right?: ReactNode }) {
  return (
    <div className="spread tab-head">
      <div className="row muted">
        <Icon name={icon} size={16} />
        <span>{text}</span>
        {hint && <Hint width={340}>{hint}</Hint>}
      </div>
      {right && <div className="row">{right}</div>}
    </div>
  );
}
