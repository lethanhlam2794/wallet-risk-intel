import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
  Button,
  DataTable,
  Tabs,
  TabsList,
  TabsTrigger,
} from "@/components";
import { ApiError, apiFetch } from "@/lib/api";
import type { ProviderConfigView } from "@/lib/types";
import { providerColumns } from "./columns";

type NetworkFilter = "all" | "ethereum" | "bsc";

function filterByNetwork(
  rows: ProviderConfigView[],
  filter: NetworkFilter,
): ProviderConfigView[] {
  if (filter === "all") return rows;
  return rows.filter((r) => r.enabled_chains?.includes(filter));
}

export function ProvidersPage() {
  const navigate = useNavigate();
  const [rows, setRows] = useState<ProviderConfigView[]>([]);
  const [filter, setFilter] = useState<NetworkFilter>("all");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [forbidden, setForbidden] = useState(false);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let cancelled = false;
    apiFetch<ProviderConfigView[]>("/api/v1/admin/provider-configs")
      .then((env) => {
        if (!cancelled) setRows(env.data ?? []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 403) {
          setForbidden(true);
        } else {
          setError(
            err instanceof Error ? err.message : "Failed to load providers.",
          );
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [reload]);

  if (forbidden) {
    return (
      <Alert variant="destructive">
        <AlertTitle>Admin role required</AlertTitle>
        <AlertDescription>
          Key valid but lacks admin role. Sign in with an admin API key to
          manage provider configurations.
        </AlertDescription>
      </Alert>
    );
  }

  if (error) {
    return (
      <Alert variant="destructive">
        <AlertTitle>Failed to load providers</AlertTitle>
        <AlertDescription>{error}</AlertDescription>
        <AlertAction>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setError(null);
              setForbidden(false);
              setLoading(true);
              setReload((n) => n + 1);
            }}
          >
            Retry
          </Button>
        </AlertAction>
      </Alert>
    );
  }

  return (
    <section data-testid="providers-table" className="space-y-3">
      <div>
        <h1 className="font-heading text-lg font-medium">Providers</h1>
        <p className="text-sm text-muted-foreground">
          Transaction-history provider configurations. Select a row to edit.
        </p>
      </div>
      <Tabs
        value={filter}
        onValueChange={(v) => setFilter(v as NetworkFilter)}
        data-testid="network-filter"
      >
        <TabsList>
          <TabsTrigger value="all" data-testid="network-filter-all">
            All ({rows.length})
          </TabsTrigger>
          <TabsTrigger
            value="ethereum"
            data-testid="network-filter-ethereum"
          >
            Ethereum ({filterByNetwork(rows, "ethereum").length})
          </TabsTrigger>
          <TabsTrigger value="bsc" data-testid="network-filter-bsc">
            BSC ({filterByNetwork(rows, "bsc").length})
          </TabsTrigger>
        </TabsList>
      </Tabs>
      <DataTable
        data={filterByNetwork(rows, filter)}
        columns={providerColumns}
        loading={loading}
        emptyMessage="No providers registered."
        pageSize={50}
        onRowClick={(row) => navigate(`/providers/${row.source_code}`)}
        getRowId={(row) => row.source_code}
      />
    </section>
  );
}
