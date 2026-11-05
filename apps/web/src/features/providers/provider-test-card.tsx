import { useState, type FormEvent } from "react";
import {
  Alert,
  AlertDescription,
  Badge,
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
} from "@/components";
import { ApiError, apiFetch } from "@/lib/api";
import type { Network } from "@/lib/types";

export interface ProbeResult {
  ok: boolean;
  enabled_for_network: boolean;
  http_status: number;
  latency_ms: number;
  head_block: number | null;
  transfers_seen: number;
  error_kind?: string;
  message?: string;
}

// TestConnectionCard exercises the provider's STORED config against the
// live upstream — the answer to "does this saved key actually work",
// independent of the requires_restart window.
export function TestConnectionCard({
  code,
  networks,
  defaultNetwork,
}: {
  code: string;
  networks: Network[];
  defaultNetwork?: string;
}) {
  const [network, setNetwork] = useState(
    defaultNetwork ?? networks[0]?.code ?? "ethereum",
  );
  const [address, setAddress] = useState("");
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<ProbeResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (running) return;
    setResult(null);
    setError(null);
    setRunning(true);
    try {
      const env = await apiFetch<ProbeResult>(
        `/api/v1/admin/provider-configs/${code}/test`,
        { method: "POST", body: { network, address } },
      );
      setResult(env.data);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code}: ${err.message}`
          : "Request failed — retry.",
      );
    } finally {
      setRunning(false);
    }
  }

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-base">Test connection</CardTitle>
        <CardDescription>
          Calls the provider upstream with the saved config and a wallet
          address — verifies the key/URL actually work now.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form
          data-testid="test-form"
          onSubmit={onSubmit}
          className="flex flex-wrap items-end gap-3"
        >
          <div className="space-y-1">
            <Label htmlFor="test-network">Network</Label>
            <Select value={network} onValueChange={setNetwork}>
              <SelectTrigger
                id="test-network"
                data-testid="test-network-select"
                className="w-44"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {networks.map((n) => (
                  <SelectItem key={n.code} value={n.code}>
                    {n.code} — {n.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="min-w-64 flex-1 space-y-1">
            <Label htmlFor="test-address">Wallet address</Label>
            <Input
              id="test-address"
              data-testid="test-address-input"
              placeholder="0x…"
              spellCheck={false}
              autoComplete="off"
              value={address}
              onChange={(e) => setAddress(e.target.value)}
              required
            />
          </div>
          <Button
            type="submit"
            variant="outline"
            data-testid="test-run-button"
            loading={running}
          >
            Run test
          </Button>
        </form>

        {error ? (
          <Alert variant="destructive" className="mt-3" data-testid="test-error">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        ) : null}

        {result ? (
          <div className="mt-3 space-y-1" data-testid="test-result">
            {result.ok ? (
              <>
                <Badge variant="success" data-testid="test-result-ok">
                  Connected
                </Badge>
                <p className="text-sm text-muted-foreground">
                  HTTP {result.http_status || "—"} · {result.latency_ms} ms ·
                  head block {result.head_block ?? "—"} ·{" "}
                  {result.transfers_seen} transfer(s) in probe window
                </p>
              </>
            ) : (
              <>
                <Badge variant="destructive" data-testid="test-result-fail">
                  {result.enabled_for_network ? "Failed" : "Disabled"}
                </Badge>
                <p className="text-sm text-muted-foreground">
                  {result.error_kind ?? "error"}
                  {result.http_status ? ` · HTTP ${result.http_status}` : ""}
                  {" · "}
                  {result.latency_ms} ms
                </p>
                {result.message ? (
                  <p className="text-sm">{result.message}</p>
                ) : null}
              </>
            )}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
