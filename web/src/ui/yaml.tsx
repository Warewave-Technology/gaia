// The YAML behind something, for whoever asks: a button that opens it in
// a modal, read only.
import { useState } from "react";
import { Button } from "./parts";
import { Modal } from "./modal";

export function YamlButton({ yaml, title, label = "View YAML" }: { yaml: string; title: string; label?: string }) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard?.writeText(yaml).catch(() => undefined);
    setCopied(true);
  };
  return (
    <>
      <Button icon="code" onClick={() => setOpen(true)} title="The object as it is stored">{label}</Button>
      {open && (
        <Modal title={title} icon="code" onClose={() => setOpen(false)} width={760}
          footer={<><Button icon="check" onClick={copy}>{copied ? "Copied" : "Copy"}</Button><Button kind="primary" onClick={() => setOpen(false)}>Close</Button></>}>
          <pre className="mono yaml-box">{yaml}</pre>
        </Modal>
      )}
    </>
  );
}
