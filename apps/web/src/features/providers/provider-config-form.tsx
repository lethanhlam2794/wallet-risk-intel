import { useState } from "react";
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Button,
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  toast,
} from "@/components";
import { ConfirmDialog } from "@/components/confirm-dialog";
import { ApiError, apiFetch } from "@/lib/api";
import { buildPatch } from "./patch";
import type {
  BaseUrlFieldState,
  ChainsFieldState,
} from "./patch";
import type {
  Network,
  ProviderConfigView,
  ProviderConfigWarning,
} from "@/lib/types";

interface ProviderConfigFormProps {
  provider: ProviderConfigView;
  networks: Network[];
  warnings: ProviderConfigWarning[];
  onSaved: (
    view: ProviderConfigView,
    warnings: ProviderConfigWarning[],
  ) => void;
  onNotFound: () => void;
}

function warningSeverity(code: string): "destructive" | "warning" {
  return code === "last_active_member" || code === "no_enabled_chains"
    ? "destructive"
    : "warning";
}

function warningTitle(code: string): string {
  switch (code) {
    case "requires_restart":
      return "Restart required";
    case "no_enabled_chains":
      return "Provider disabled on every chain";
    case "last_active_member":
      return "Last active member deactivated";
    default:
      return "Warning";
  }
}

export function ProviderConfigForm({
  provider,
  networks,
  warnings,
  onSaved,
  onNotFound,
}: ProviderConfigFormProps) {
  const [apiKeyInput, setApiKeyInput] = useState("");
  const [baseUrl, setBaseUrl] = useState<BaseUrlFieldState>({
    input: provider.base_url ?? "",
    cleared: false,
  });
  const [chains, setChains] = useState<ChainsFieldState>({
    kind: "untouched",
  });
  const [statusVal, setStatusVal] = useState<"active" | "inactive">(
    provider.status,
  );
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [baseUrlError, setBaseUrlError] = useState<string | null>(null);
  const [clearOpen, setClearOpen] = useState(false);
  const [emptyChainsOpen, setEmptyChainsOpen] = useState(false);

  const apiKeyInvalid = apiKeyInput.startsWith("enc:");

  const isChecked = (code: string): boolean =>
    chains.kind === "untouched"
      ? (provider.enabled_chains?.includes(code) ?? false)
      : chains.value.includes(code);

  const currentSelection = (): Set<string> =>
    chains.kind === "untouched"
      ? new Set(provider.enabled_chains ?? [])
      : new Set(chains.value);

  // PUT body = dirty fields only (see patch.ts for the elision rules).
  const patch = buildPatch(
    { apiKeyInput, baseUrl, chains, statusVal },
    provider,
  );
  const patchEmpty = Object.keys(patch).length === 0;

  function onChainToggle(code: string, checked: boolean) {
    const next = currentSelection();
    if (checked) next.add(code);
    else next.delete(code);
    if (next.size === 0) {
      setEmptyChainsOpen(true);
      return;
    }
    setChains({ kind: "set", value: [...next] });
  }

  async function submit() {
    if (saving || patchEmpty || apiKeyInvalid) return;
    setSaving(true);
    setFormError(null);
    setBaseUrlError(null);
    try {
      const env = await apiFetch<ProviderConfigView>(
        `/api/v1/admin/provider-configs/${provider.source_code}`,
        { method: "PUT", body: patch },
      );
      onSaved(env.data, env.warnings ?? []);
    } catch (e) {
      if (e instanceof ApiError) {
        if (e.status === 400) {
          if (e.allowedHosts) {
            setBaseUrlError(
              `${e.message} Allowed hosts: ${e.allowedHosts.join(", ")}`,
            );
          } else {
            setFormError(e.message);
          }
        } else if (e.status === 404) {
          onNotFound();
        } else if (e.status === 429 || e.status >= 500) {
          toast.error(e.message);
          setFormError(`${e.message} — retry.`);
        } else {
          setFormError(e.message);
        }
      } else {
        toast.error("Save failed — retry.");
        setFormError("Save failed — retry.");
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card data-testid="config-form">
      <CardHeader>
        <CardTitle>Configuration</CardTitle>
        <CardDescription>
          Only changed fields are sent. Empty strings and empty chain lists
          are explicit overrides — they do not fall back to environment
          defaults.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1">
          <Label htmlFor="api-key-input">API key</Label>
          <Input
            id="api-key-input"
            data-testid="apikey-input"
            type="password"
            autoComplete="new-password"
            placeholder={
              provider.api_key.set
                ? `set ••••${provider.api_key.last4 ?? ""}`
                : "not set"
            }
            value={apiKeyInput}
            onChange={(e) => setApiKeyInput(e.target.value)}
            aria-invalid={apiKeyInvalid || undefined}
          />
          <p className="text-xs text-muted-foreground">
            Write-only — never displayed. Leave blank to keep the stored
            key.
          </p>
          {apiKeyInvalid ? (
            <p
              data-testid="field-error-api_key"
              className="text-xs text-destructive"
            >
              Values starting with &quot;enc:&quot; are not accepted.
            </p>
          ) : null}
        </div>

        <div className="space-y-1">
          <Label htmlFor="base-url-input">Base URL</Label>
          <div className="flex items-center gap-2">
            <Input
              id="base-url-input"
              data-testid="baseurl-input"
              value={baseUrl.input}
              onChange={(e) =>
                setBaseUrl({ input: e.target.value, cleared: false })
              }
              placeholder="env default"
              aria-invalid={baseUrlError !== null || undefined}
            />
            {provider.base_url !== null && !baseUrl.cleared ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                data-testid="baseurl-clear"
                onClick={() => setClearOpen(true)}
              >
                Clear
              </Button>
            ) : null}
          </div>
          <p className="text-xs text-muted-foreground">
            Blanking the field leaves the stored value unchanged — use
            Clear + confirm to send an explicit empty override.
          </p>
          {baseUrlError ? (
            <p
              data-testid="field-error-base_url"
              className="text-xs text-destructive"
            >
              {baseUrlError}
            </p>
          ) : null}
        </div>

        <fieldset
          data-testid="chains-fieldset"
          className="space-y-2 rounded-lg border p-3"
        >
          <legend className="px-1 text-sm font-medium">
            Enabled chains
          </legend>
          {provider.enabled_chains === null &&
          chains.kind === "untouched" ? (
            <p className="text-xs text-muted-foreground">
              env default — no override stored. Toggling any chain stores
              an explicit list; an empty selection disables this provider
              on every chain and cannot revert to env default from this
              form.
            </p>
          ) : null}
          {networks.map((n) => (
            <label
              key={n.code}
              className="flex items-center gap-2 text-sm"
            >
              <input
                type="checkbox"
                data-testid={`chain-check-${n.code}`}
                checked={isChecked(n.code)}
                onChange={(e) => onChainToggle(n.code, e.target.checked)}
                className="size-4 accent-primary"
              />
              <span className="font-mono">{n.code}</span>
              <span className="text-muted-foreground">{n.name}</span>
            </label>
          ))}
        </fieldset>

        <div className="space-y-1">
          <Label>Status</Label>
          <Select
            value={statusVal}
            onValueChange={(v) =>
              setStatusVal(v as "active" | "inactive")
            }
          >
            <SelectTrigger data-testid="status-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="active">Active</SelectItem>
              <SelectItem value="inactive">Inactive</SelectItem>
            </SelectContent>
          </Select>
          <p className="text-xs text-muted-foreground">
            The only runtime-effective field (takes effect within ~30s).
          </p>
        </div>

        {formError ? (
          <Alert variant="destructive" data-testid="form-error">
            <AlertDescription>{formError}</AlertDescription>
          </Alert>
        ) : null}

        {warnings.length > 0 ? (
          <div data-testid="warnings-region" className="space-y-2">
            {warnings.map((w, i) => (
              <Alert
                key={`${w.code}-${i}`}
                variant={warningSeverity(w.code)}
                data-testid={`warning-${w.code}`}
                data-severity={warningSeverity(w.code)}
              >
                <AlertTitle>{warningTitle(w.code)}</AlertTitle>
                <AlertDescription>{w.message}</AlertDescription>
              </Alert>
            ))}
          </div>
        ) : null}

        <Button
          data-testid="save-button"
          disabled={saving || patchEmpty || apiKeyInvalid}
          loading={saving}
          onClick={submit}
        >
          Save changes
        </Button>
      </CardContent>

      <ConfirmDialog
        open={clearOpen}
        onOpenChange={setClearOpen}
        title="Clear stored base URL?"
        description="Sends an explicit empty override — the stored value is cleared and there is no env fallback."
        confirmLabel="Clear"
        onConfirm={() => setBaseUrl({ input: "", cleared: true })}
      />
      <ConfirmDialog
        open={emptyChainsOpen}
        onOpenChange={setEmptyChainsOpen}
        title="Disable on every chain?"
        description="An empty selection disables this provider on every chain and cannot revert to env default from this form."
        confirmLabel="Disable everywhere"
        onConfirm={() => setChains({ kind: "set", value: [] })}
      />
    </Card>
  );
}
