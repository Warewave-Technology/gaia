// Confirming who you are just before a decision (design 11.2): a passkey,
// or a TOTP code for someone without one. Gaia marks the session strong
// for ten minutes; the decision follows at once.
import { startAuthentication } from "@simplewebauthn/browser";
import { api, ApiError } from "../api/client";

export type StepUpNeed = { kind: "done" } | { kind: "totp" };

// stepUp asks for the passkey. It answers "totp" when the person has no
// passkey, and the caller then asks for a code and calls stepUpTOTP.
export async function stepUp(): Promise<StepUpNeed> {
  let begin: { flow: string; options: { publicKey: Parameters<typeof startAuthentication>[0]["optionsJSON"] } };
  try {
    begin = await api("/auth/step-up/begin", { method: "POST" });
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) return { kind: "totp" };
    throw e;
  }
  const credential = await startAuthentication({ optionsJSON: begin.options.publicKey });
  await api("/auth/step-up/finish", { method: "POST", body: { flow: begin.flow, credential } });
  return { kind: "done" };
}

export async function stepUpTOTP(code: string): Promise<void> {
  await api("/auth/step-up/totp", { method: "POST", body: { code } });
}

// withStepUp does what needs a strong session, confirming first when Gaia
// asks for it.
export async function decide<T>(doIt: () => Promise<T>, confirm: () => Promise<void>): Promise<T> {
  try {
    return await doIt();
  } catch (e) {
    if (!(e instanceof ApiError && e.stepUp)) throw e;
  }
  await confirm();
  return doIt();
}
