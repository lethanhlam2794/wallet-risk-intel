// Types mirror apps/api/api/openapi.yaml exactly (provider-config admin
// surface + networks). Do not widen or re-shape.

export interface ApiKeyMask {
  set: boolean;
  last4: string | null;
}

export type RestartField = "api_key" | "base_url" | "enabled_chains";

export interface ProviderConfigView {
  source_code: string;
  configured: boolean;
  api_key: ApiKeyMask;
  base_url: string | null;
  enabled_chains: string[] | null;
  status: "active" | "inactive";
  catalog_active: boolean;
  effective_active: boolean;
  requires_restart: RestartField[];
  updated_by: string | null;
  updated_at: string | null;
}

// Whitelisted writable fields; an absent key leaves the column untouched.
// "" and [] are explicit overrides (no env fallback).
export interface ProviderConfigPatch {
  api_key?: string;
  base_url?: string;
  enabled_chains?: string[];
  status?: "active" | "inactive";
}

export interface ProviderConfigWarning {
  code: string;
  field?: string;
  network?: string;
  message: string;
}

export interface ProviderConfigChange {
  id: string;
  field: string;
  previous_value: unknown;
  new_value: unknown;
  actor_id: string | null;
  actor_type: string;
  changed_at: string;
}

export interface ProviderConfigChangesPage {
  items: ProviderConfigChange[];
  next_cursor: string;
}

export interface Network {
  code: string;
  name: string;
  chain_id: string;
  namespace: string;
}

export interface Envelope<T> {
  data: T;
  warnings?: ProviderConfigWarning[];
}

export interface LoginResponse {
  token: string;
  token_type: string;
  expires_at: string;
  user: { username: string; role: string };
}
