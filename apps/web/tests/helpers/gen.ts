import type {
  Network,
  ProviderConfigPatch,
  ProviderConfigView,
  RestartField,
} from "@/lib/types";
import type { FormFields } from "@/features/providers/patch";

// Deterministic PRNG — same seed ⇒ same sequence across runs/machines.
export function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function pick<T>(rand: () => number, arr: readonly T[]): T {
  return arr[Math.floor(rand() * arr.length)]!;
}

export function randBool(rand: () => number): boolean {
  return rand() < 0.5;
}

const CHAIN_POOL = [
  "ethereum",
  "bsc",
  "polygon",
  "arbitrum",
  "optimism",
  "base",
] as const;

const RESTART_POOL: readonly RestartField[] = [
  "api_key",
  "base_url",
  "enabled_chains",
];

export function genChains(
  rand: () => number,
): string[] | null {
  const r = rand();
  if (r < 0.25) return null;
  const size = Math.floor(rand() * 4); // 0..3 — includes explicit []
  const shuffled = [...CHAIN_POOL].sort(() => rand() - 0.5);
  return shuffled.slice(0, size);
}

export function genProvider(
  rand: () => number,
  code: string,
): ProviderConfigView {
  const keySet = randBool(rand);
  return {
    source_code: code,
    configured: randBool(rand),
    api_key: {
      set: keySet,
      last4: keySet ? String(1000 + Math.floor(rand() * 9000)) : null,
    },
    base_url: randBool(rand)
      ? `https://api-${code}.example.com`
      : null,
    enabled_chains: genChains(rand),
    status: randBool(rand) ? "active" : "inactive",
    catalog_active: randBool(rand),
    effective_active: randBool(rand),
    requires_restart: randBool(rand)
      ? []
      : [pick(rand, RESTART_POOL)],
    updated_by: randBool(rand) ? "ops-admin" : null,
    updated_at: "2026-11-04T23:00:00Z",
  };
}

export function genNetworks(): Network[] {
  return CHAIN_POOL.map((code, i) => ({
    code,
    name: `Network ${code}`,
    chain_id: `eip155:${i + 1}`,
    namespace: "eip155",
  }));
}

// A random form-edit scenario: which fields are dirty and with what
// values — sometimes deliberately equal to stored to exercise elision.
export function genFormFields(
  rand: () => number,
  stored: ProviderConfigView,
): FormFields {
  const apiKeyInput = randBool(rand)
    ? ""
    : `key-${Math.floor(rand() * 1e6)}`;

  const baseUrl = (() => {
    const r = rand();
    if (r < 0.3) return { input: stored.base_url ?? "", cleared: false };
    if (r < 0.45) return { input: "", cleared: true };
    if (r < 0.6) return { input: "", cleared: false };
    if (r < 0.75)
      return { input: stored.base_url ?? "", cleared: false }; // equal ⇒ elide
    return {
      input: `https://new-${Math.floor(rand() * 1e6)}.example.com`,
      cleared: false,
    };
  })();

  const chains: FormFields["chains"] = (() => {
    const r = rand();
    if (r < 0.4) return { kind: "untouched" };
    if (r < 0.55 && stored.enabled_chains !== null)
      // reordered-but-equal copy ⇒ elide
      return { kind: "set", value: [...stored.enabled_chains].reverse() };
    if (r < 0.7) return { kind: "set", value: [] };
    return {
      kind: "set",
      value: [
        ...new Set(
          Array.from({ length: 1 + Math.floor(rand() * 3) }, () =>
            pick(rand, CHAIN_POOL),
          ),
        ),
      ],
    };
  })();

  const statusVal: "active" | "inactive" = randBool(rand)
    ? stored.status
    : stored.status === "active"
      ? "inactive"
      : "active";

  return { apiKeyInput, baseUrl, chains, statusVal };
}

// Independent expected-key computation (test-side oracle — NOT a copy
// of buildPatch internals, per anti-circular test design).
export function expectedPatchKeys(
  fields: FormFields,
  stored: ProviderConfigView,
): Array<keyof ProviderConfigPatch> {
  const keys: Array<keyof ProviderConfigPatch> = [];
  if (fields.apiKeyInput !== "") keys.push("api_key");
  if (
    fields.baseUrl.cleared ||
    (fields.baseUrl.input !== "" &&
      fields.baseUrl.input !== stored.base_url)
  )
    keys.push("base_url");
  if (fields.chains.kind === "set") {
    const s = stored.enabled_chains;
    const equal =
      s !== null &&
      s.length === fields.chains.value.length &&
      fields.chains.value.every((c) => s.includes(c));
    if (!equal) keys.push("enabled_chains");
  }
  if (fields.statusVal !== stored.status) keys.push("status");
  return keys.sort();
}

export function expectedPatchValues(
  fields: FormFields,
): Partial<ProviderConfigPatch> {
  const out: Partial<ProviderConfigPatch> = {};
  if (fields.apiKeyInput !== "") out.api_key = fields.apiKeyInput;
  if (fields.baseUrl.cleared) out.base_url = "";
  else if (fields.baseUrl.input !== "") out.base_url = fields.baseUrl.input;
  if (fields.chains.kind === "set")
    out.enabled_chains = fields.chains.value;
  out.status = fields.statusVal;
  return out;
}

export { RESTART_POOL };
