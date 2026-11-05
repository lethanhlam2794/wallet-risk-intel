import type {
  ProviderConfigPatch,
  ProviderConfigView,
} from "@/lib/types";

// Per-field edit state for the config form. `untouched` fields emit no
// PATCH key; `set` fields emit their value (subject to value-elision);
// `cleared` emits the explicit-empty override ("" only — enabled_chains
// reaches [] through a normal `set` whose array is empty).
export type ChainsFieldState =
  | { kind: "untouched" }
  | { kind: "set"; value: string[] };

export interface BaseUrlFieldState {
  input: string;
  cleared: boolean;
}

export interface FormFields {
  apiKeyInput: string;
  baseUrl: BaseUrlFieldState;
  chains: ChainsFieldState;
  statusVal: "active" | "inactive";
}

// null (env default) is never equal to an explicit array — [] disables
// everywhere and cannot revert to env from this form.
export function chainsEqual(
  edited: string[],
  stored: string[] | null,
): boolean {
  if (stored === null || edited.length !== stored.length) return false;
  const s = new Set(stored);
  return edited.every((c) => s.has(c));
}

// PUT body = dirty fields only. Absent key = untouched column; "" and []
// are explicit overrides. `set` fields whose value equals stored are
// elided (strict-equal for scalars, set-equality for chains).
export function buildPatch(
  fields: FormFields,
  stored: ProviderConfigView,
): ProviderConfigPatch {
  const patch: ProviderConfigPatch = {};
  if (fields.apiKeyInput !== "") patch.api_key = fields.apiKeyInput;
  if (fields.baseUrl.cleared) patch.base_url = "";
  else if (
    fields.baseUrl.input !== "" &&
    fields.baseUrl.input !== stored.base_url
  )
    patch.base_url = fields.baseUrl.input;
  if (
    fields.chains.kind === "set" &&
    !chainsEqual(fields.chains.value, stored.enabled_chains)
  )
    patch.enabled_chains = fields.chains.value;
  if (fields.statusVal !== stored.status) patch.status = fields.statusVal;
  return patch;
}
