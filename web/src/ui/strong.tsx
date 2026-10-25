// useStrong runs something that needs a recent confirmation (design
// 11.2): it asks for the passkey when Gaia says so, or for a TOTP code
// when the person has no passkey.
import { useCallback, useState, type FormEvent } from "react";
import { decide, stepUp, stepUpTOTP } from "../lib/stepup";
import { Button } from "./parts";
import { Modal } from "./modal";

type Ask = { resolve: (code: string) => void; reject: (e: Error) => void };

export function useStrong() {
  const [ask, setAsk] = useState<Ask | null>(null);
  const run = useCallback(<T,>(fn: () => Promise<T>) => decide(fn, async () => {
    const r = await stepUp();
    if (r.kind === "totp") {
      const code = await new Promise<string>((resolve, reject) => setAsk({ resolve, reject }));
      setAsk(null);
      await stepUpTOTP(code);
    }
  }), []);
  const prompt = ask ? <TotpPrompt ask={ask} onClose={() => setAsk(null)} /> : null;
  return { run, prompt };
}

function TotpPrompt({ ask, onClose }: { ask: Ask; onClose: () => void }) {
  const [code, setCode] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    ask.resolve(code);
  };
  const cancel = () => {
    ask.reject(new Error("Confirmation cancelled."));
    onClose();
  };
  return (
    <Modal title="Confirm it is you" icon="key" onClose={cancel} onSubmit={submit} width={440}
      footer={<><Button onClick={cancel}>Cancel</Button><Button type="submit" kind="primary" icon="key">Confirm</Button></>}>
      <p className="muted" style={{ margin: 0 }}>This needs a recent confirmation. You have no passkey, so enter the code from your authenticator app.</p>
      <label className="field">
        <span>Code</span>
        <input className="input mono" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} required />
      </label>
    </Modal>
  );
}
