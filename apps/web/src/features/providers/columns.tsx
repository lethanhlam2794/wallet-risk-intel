import { Badge } from "@/components";
import type { ColumnDef } from "@/components/data-table";
import type { ProviderConfigView } from "@/lib/types";

export const providerColumns: ColumnDef<ProviderConfigView>[] = [
  {
    accessorKey: "source_code",
    header: "Provider",
    cell: ({ row }) => (
      <span
        data-testid={`provider-row-${row.original.source_code}`}
        className="font-medium"
      >
        {row.original.source_code}
      </span>
    ),
  },
  {
    id: "status",
    header: "Status",
    cell: ({ row }) =>
      row.original.effective_active ? (
        <Badge variant="success">Active</Badge>
      ) : (
        <Badge variant="secondary">Inactive</Badge>
      ),
  },
  {
    id: "configured",
    header: "Config",
    cell: ({ row }) =>
      row.original.configured ? (
        <span className="text-muted-foreground">stored</span>
      ) : (
        <Badge variant="outline">defaults/env</Badge>
      ),
  },
  {
    id: "api_key",
    header: "API key",
    cell: ({ row }) => {
      const key = row.original.api_key;
      return key.set ? (
        <span>{`••••${key.last4 ?? ""}`}</span>
      ) : (
        <span className="text-muted-foreground">not set</span>
      );
    },
  },
  {
    id: "base_url",
    header: "Base URL",
    cell: ({ row }) =>
      row.original.base_url ?? (
        <span className="text-muted-foreground">—</span>
      ),
  },
  {
    id: "enabled_chains",
    header: "Chains",
    cell: ({ row }) => {
      const chains = row.original.enabled_chains;
      if (chains === null)
        return <span className="text-muted-foreground">env default</span>;
      if (chains.length === 0)
        return <span className="text-muted-foreground">none</span>;
      return chains.join(", ");
    },
  },
  {
    id: "restart",
    header: "Restart",
    cell: ({ row }) =>
      row.original.requires_restart.length > 0 ? (
        <Badge
          variant="warning"
          data-testid={`restart-badge-${row.original.source_code}`}
        >
          restart required
        </Badge>
      ) : null,
  },
];
