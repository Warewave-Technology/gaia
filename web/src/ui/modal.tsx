// A modal dialog: adding things and confirming decisions happen here,
// opened from a button at the top right of their card.
import { useEffect, useId, useRef, type FormEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Icon, type IconName } from "./icons";

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function Modal({ title, icon, onClose, onSubmit, children, footer, width = 520, tone }: {
  title: ReactNode;
  icon?: IconName;
  onClose: () => void;
  // With onSubmit the body is a form: Enter submits it.
  onSubmit?: (e: FormEvent) => void;
  children: ReactNode;
  footer?: ReactNode;
  width?: number;
  tone?: "yellow" | "red";
}) {
  const id = useId();
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const before = document.activeElement as HTMLElement | null;
    const first = box.current?.querySelector<HTMLElement>("input:not([readonly]):not([type=checkbox]), textarea, select") ?? box.current?.querySelector<HTMLElement>(FOCUSABLE);
    first?.focus();
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== "Tab" || !box.current) return;
      const els = [...box.current.querySelectorAll<HTMLElement>(FOCUSABLE)];
      if (els.length === 0) return;
      const [a, z] = [els[0]!, els[els.length - 1]!];
      if (e.shiftKey && document.activeElement === a) {
        e.preventDefault();
        z.focus();
      } else if (!e.shiftKey && document.activeElement === z) {
        e.preventDefault();
        a.focus();
      }
    };
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("keydown", key);
      before?.focus?.();
    };
  }, [onClose]);
  const accent = tone === "red" ? "var(--red)" : "var(--yellow)";
  const body = (
    <>
      <div className="modal-head">
        <div className="row" style={{ gap: 10 }}>
          {icon && <span style={{ color: accent }}><Icon name={icon} size={18} /></span>}
          <h2 id={id}>{title}</h2>
        </div>
        <button type="button" className="icon-btn" aria-label="Kapat" onClick={onClose}><Icon name="x" size={18} /></button>
      </div>
      <div className="modal-body">{children}</div>
      {footer && <div className="modal-foot">{footer}</div>}
    </>
  );
  return createPortal(
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={box} className="modal" role="dialog" aria-modal="true" aria-labelledby={id} style={{ width, borderTopColor: accent }}>
        {onSubmit ? <form onSubmit={onSubmit}>{body}</form> : body}
      </div>
    </div>,
    document.body,
  );
}

// Switch is an on/off control that acts at once.
export function Switch({ on, onChange, label, disabled }: { on: boolean; onChange: (on: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <button type="button" role="switch" aria-checked={on} aria-label={label} className={on ? "switch on" : "switch"} disabled={disabled} onClick={() => onChange(!on)}>
      <span className="switch-knob" />
    </button>
  );
}
