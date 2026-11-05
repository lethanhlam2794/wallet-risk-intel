import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeftIcon } from "lucide-react";
import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
  Badge,
  Button,
  EmptyState,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@/components";
import { ApiError, apiFetch } from "@/lib/api";
import type {
  Network,
  ProviderConfigView,
  ProviderConfigWarning,
} from "@/lib/types";
import { ProviderConfigForm } from "./provider-config-form";
import { ProviderHistory } from "./provider-history";
import { TestConnectionCard } from "./provider-test-card";

export function ProviderDetailPage() {
  const { code = "" } = useParams();
  const [view, setView] = useState<ProviderConfigView | null>(null);
  const [networks, setNetworks] = useState<Network[]>([]);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  const [warnings, setWarnings] = useState<ProviderConfigWarning[]>([]);
  const [formEpoch, setFormEpoch] = useState(0);
  const [tab, setTab] = useState("config");
  const [historyActivated, setHistoryActivated] = useState(false);

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      apiFetch<ProviderConfigView>(
        `/api/v1/admin/provider-configs/${code}`,
      ),
      apiFetch<Network[]>("/api/v1/networks"),
    ])
      .then(([v, n]) => {
        if (cancelled) return;
        setView(v.data);
        setNetworks(n.data);
      })
      .catch((e: unknown) => {
        if (cancelled) return;
        if (e instanceof ApiError && e.status === 404) setNotFound(true);
        else
          setError(
            e instanceof Error ? e.message : "Failed to load provider.",
          );
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [code, reload]);

  if (loading) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }

  if (notFound) {
    return (
      <EmptyState
        title="Provider not found"
        description={`No provider configuration exists for "${code}".`}
      />
    );
  }

  if (error || !view) {
    return (
      <Alert variant="destructive">
        <AlertTitle>Failed to load provider</AlertTitle>
        <AlertDescription>
          {error ?? "Unknown error."}
        </AlertDescription>
        <AlertAction>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setError(null);
              setNotFound(false);
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
    <section className="space-y-4">
      <header className="flex flex-wrap items-center gap-2">
        <Button
          variant="ghost"
          size="sm"
          data-testid="back-to-providers"
          asChild
        >
          <Link to="/providers">
            <ArrowLeftIcon />
            Providers
          </Link>
        </Button>
        <h1 className="font-heading text-lg font-medium">
          {view.source_code}
        </h1>
        {!view.configured ? (
          <Badge variant="outline">defaults/env</Badge>
        ) : null}
        {view.requires_restart.length > 0 ? (
          <Badge variant="warning" data-testid="restart-badge">
            restart required
          </Badge>
        ) : null}
      </header>

      <TestConnectionCard
        code={view.source_code}
        networks={networks}
        defaultNetwork={view.enabled_chains?.[0]}
      />

      <Tabs
        value={tab}
        onValueChange={(v) => {
          setTab(v);
          if (v === "history") setHistoryActivated(true);
        }}
        data-testid="provider-tabs"
      >
        <TabsList>
          <TabsTrigger value="config" data-testid="tab-config">
            Config
          </TabsTrigger>
          <TabsTrigger value="history" data-testid="tab-history">
            History
          </TabsTrigger>
        </TabsList>
        {/* forceMount keeps dirty form + accumulated history alive across
            switches; radix-ui 1.8 does not hide inactive forceMounted
            panels, so data-[state=inactive]:hidden does it instead. */}
        <TabsContent
          value="config"
          forceMount
          className="data-[state=inactive]:hidden"
        >
          <ProviderConfigForm
            key={`${view.source_code}-${formEpoch}`}
            provider={view}
            networks={networks}
            warnings={warnings}
            onSaved={(v, w) => {
              setView(v);
              setWarnings(w);
              setFormEpoch((e) => e + 1);
            }}
            onNotFound={() => setNotFound(true)}
          />
        </TabsContent>
        <TabsContent
          value="history"
          forceMount
          className="data-[state=inactive]:hidden"
        >
          <ProviderHistory code={view.source_code} active={historyActivated} />
        </TabsContent>
      </Tabs>
    </section>
  );
}
