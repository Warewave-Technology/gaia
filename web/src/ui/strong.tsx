// useStrong runs something that needs a recent confirmation (design
// 11.2): it asks for the passkey when Gaia says so, or for a TOTP code
// when the person has no passkey.
import { useCallback, useState, type FormEvent } from "react";
import { decide, stepUp, stepUpTOTP } from "../lib/stepup";
import { Button } from "./parts";

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
  return (
    <form onSubmit={submit} className="card" style={{ borderLeft: "4px solid var(--yellow)", padding: "16px 20px", display: "flex", flexWrap: "wrap", alignItems: "flex-end", gap: 12 }}>
      <label className="field" style={{ flex: "1 1 260px" }}>
        <span>Bu işlem için kimliğinizi doğrulayın: doğrulama uygulamanızdaki kod</span>
        <input className="input mono" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} autoFocus required />
      </label>
      <Button onClick={() => { ask.reject(new Error("Doğrulama iptal edildi.")); onClose(); }}>Vazgeç</Button>
      <Button type="submit" kind="primary" icon="key">Doğrula</Button>
    </form>
  );
}
