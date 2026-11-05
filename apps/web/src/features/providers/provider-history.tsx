import { useEffect, useState } from "react";
import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  EmptyState,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components";
import { apiFetch } from "@/lib/api";
import type {
  ProviderConfigChange,
  ProviderConfigChangesPage,
} from "@/lib/types";

const PAGE_LIMIT = 50;

// previous_value/new_value are untyped in the spec — normalize every
// shape; api_key values are always the masked {set,last4} shape.
export function formatChangeValue(field: string, value: unknown): string {
  if (field === "api_key") {
    if (typeof value === "object" && value !== null) {
      const v = value as { set?: unknown; last4?: unknown };
      return v.set
        ? `••••${typeof v.last4 === "string" ? v.last4 : ""}`
        : "not set";
    }
    return "not set";
  }
  if (value === null || value === undefined) return "—";
  if (typeof value === "string") return value === "" ? "(empty)" : value;
  if (Array.isArray(value))
    return value.length === 0 ? "[]" : value.join(", ");
  return JSON.stringify(value);
}

function formatTimestamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.toISOString().slice(0, 19).replace("T", " ")} UTC`;
}

interface ProviderHistoryProps {
  code: string;
  active: boolean;
}

export function ProviderHistory({ code, active }: ProviderHistoryProps) {
  const [items, setItems] = useState<ProviderConfigChange[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [prevCode, setPrevCode] = useState(code);

  // Provider switched under a mounted tab — reset accumulated state.
  if (prevCode !== code) {
    setPrevCode(code);
    setItems([]);
    setNextCursor(null);
    setLoaded(false);
    setError(null);
  }

  // Derived first-load indicator — no synchronous setState in the effect.
  const loading = active && !loaded && error === null;

  async function fetchPage(
    cursor: string | null,
  ): Promise<ProviderConfigChangesPage> {
    const params = new URLSearchParams({ limit: String(PAGE_LIMIT) });
    if (cursor) params.set("cursor", cursor);
    const env = await apiFetch<ProviderConfigChangesPage>(
      `/api/v1/admin/provider-configs/${code}/changes?${params.toString()}`,
    );
    return env.data;
  }

  // Lazy: fetch page 1 only when the History tab is first activated
  // (and again if `code` changes while active). forceMount keeps state
  // across tab switches, so loaded pages persist.
  useEffect(() => {
    if (!active || loaded) return;
    let cancelled = false;
    fetchPage(null)
      .then((page) => {
        if (cancelled) return;
        setItems(page.items);
        setNextCursor(page.next_cursor || null);
        setLoaded(true);
      })
      .catch((e: unknown) => {
        if (!cancelled)
          setError(
            e instanceof Error ? e.message : "Failed to load history.",
          );
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, loaded, code]);

  async function retry() {
    setError(null);
    try {
      const page = await fetchPage(items.length === 0 ? null : nextCursor);
      if (items.length === 0) {
        setItems(page.items);
        setLoaded(true);
      } else {
        setItems((prev) => [...prev, ...page.items]);
      }
      setNextCursor(page.next_cursor || null);
    } catch (e) {
      setError(
        e instanceof Error ? e.message : "Failed to load history.",
      );
    }
  }

  async function loadMore() {
    if (!nextCursor || loadingMore) return;
    setLoadingMore(true);
    try {
      const page = await fetchPage(nextCursor);
      setItems((prev) => [...prev, ...page.items]);
      setNextCursor(page.next_cursor || null);
    } catch (e) {
      setError(
        e instanceof Error ? e.message : "Failed to load history.",
      );
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Audit history</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {error ? (
          <Alert variant="destructive">
            <AlertTitle>Failed to load history</AlertTitle>
            <AlertDescription>{error}</AlertDescription>
            <AlertAction>
              <Button variant="outline" size="sm" onClick={retry}>
                Retry
              </Button>
            </AlertAction>
          </Alert>
        ) : null}
        <Table data-testid="history-table">
          <TableHeader>
            <TableRow>
              <TableHead>Field</TableHead>
              <TableHead>Previous → New</TableHead>
              <TableHead>Actor</TableHead>
              <TableHead>Changed at</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loaded && items.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4}>
                  <div data-testid="history-empty">
                    <EmptyState title="No changes recorded." />
                  </div>
                </TableCell>
              </TableRow>
            ) : (
              items.map((c) => (
                <TableRow key={c.id}>
                  <TableCell className="font-mono">{c.field}</TableCell>
                  <TableCell>
                    {formatChangeValue(c.field, c.previous_value)} →{" "}
                    {formatChangeValue(c.field, c.new_value)}
                  </TableCell>
                  <TableCell title={c.actor_type}>
                    {c.actor_id ?? "—"}{" "}
                    <span className="text-muted-foreground">
                      ({c.actor_type})
                    </span>
                  </TableCell>
                  <TableCell>{formatTimestamp(c.changed_at)}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
        {loading ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : null}
        {nextCursor ? (
          <Button
            data-testid="load-more"
            variant="outline"
            size="sm"
            loading={loadingMore}
            onClick={loadMore}
          >
            Load more
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}
