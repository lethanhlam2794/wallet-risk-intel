import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  MemoryRouter,
  Route,
  Routes,
  useLocation,
  useParams,
} from "react-router-dom";
import { AuthProvider } from "@/auth/auth-context";
import { ProvidersPage } from "@/features/providers/providers-page";
import type { ProviderConfigView } from "@/lib/types";

const fetchMock = vi.fn<typeof fetch>();

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function makeProvider(
  source_code: string,
  overrides: Partial<ProviderConfigView> = {},
): ProviderConfigView {
  return {
    source_code,
    configured: true,
    api_key: { set: false, last4: null },
    base_url: null,
    enabled_chains: null,
    status: "active",
    catalog_active: true,
    effective_active: true,
    requires_restart: [],
    updated_by: null,
    updated_at: null,
    ...overrides,
  };
}

const PROVIDER_CODES = [
  "alchemy",
  "blockscout",
  "ankr",
  "covalent",
  "moralis",
  "tatum",
  "bitquery",
  "oklink",
  "tokenview",
  "chainbase",
  "chainscan",
  "nodereal",
];

const PROVIDERS: ProviderConfigView[] = PROVIDER_CODES.map((code) =>
  makeProvider(code),
);

function LocationDisplay() {
  const loc = useLocation();
  return <div data-testid="location">{loc.pathname}</div>;
}

function DetailStub() {
  const { code } = useParams();
  return <div data-testid="detail">{`detail:${code}`}</div>;
}

function renderProviders() {
  return render(
    <MemoryRouter initialEntries={["/providers"]}>
      <AuthProvider>
        <Routes>
          <Route
            path="/providers"
            element={
              <>
                <ProvidersPage />
                <LocationDisplay />
              </>
            }
          />
          <Route path="/providers/:code" element={<DetailStub />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  sessionStorage.clear();
  localStorage.clear();
  sessionStorage.setItem("cw.admin.token", "test-token");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("providers list", () => {
  it("renders all 12 provider codes from the envelope", async () => {
    fetchMock.mockResolvedValue(json({ data: PROVIDERS }));
    renderProviders();
    for (const code of PROVIDER_CODES) {
      expect(
        await screen.findByTestId(`provider-row-${code}`),
      ).toBeInTheDocument();
    }
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const url = String(fetchMock.mock.calls[0]![0]);
    expect(url).toBe("/api/v1/admin/provider-configs");
  });

  it("shows skeleton rows while loading", async () => {
    let resolve: (r: Response) => void = () => {};
    fetchMock.mockReturnValue(
      new Promise<Response>((r) => {
        resolve = r;
      }),
    );
    renderProviders();
    expect(
      await screen.findAllByTestId("datatable-skeleton-row"),
    ).not.toHaveLength(0);
    resolve(json({ data: PROVIDERS }));
    expect(
      await screen.findByTestId("provider-row-chainscan"),
    ).toBeInTheDocument();
  });

  it("marks configured:false rows as defaults/env", async () => {
    const rows = PROVIDERS.map((p) =>
      p.source_code === "chainscan"
        ? { ...p, configured: false }
        : p,
    );
    fetchMock.mockResolvedValue(json({ data: rows }));
    renderProviders();
    const cell = await screen.findByTestId("provider-row-chainscan");
    const row = cell.closest("tr")!;
    expect(row).toHaveTextContent("defaults/env");
  });

  it("renders masked api_key shapes — ••••last4 or 'not set', never raw", async () => {
    const rows = PROVIDERS.map((p) => {
      if (p.source_code === "alchemy")
        return { ...p, api_key: { set: true, last4: "4242" } };
      if (p.source_code === "ankr")
        return { ...p, api_key: { set: true, last4: null } };
      return p;
    });
    fetchMock.mockResolvedValue(json({ data: rows }));
    renderProviders();
    const alchemy = (
      await screen.findByTestId("provider-row-alchemy")
    ).closest("tr")!;
    expect(alchemy).toHaveTextContent("••••4242");
    const ankr = (
      await screen.findByTestId("provider-row-ankr")
    ).closest("tr")!;
    expect(ankr).toHaveTextContent("••••");
    const moralis = (
      await screen.findByTestId("provider-row-moralis")
    ).closest("tr")!;
    expect(moralis).toHaveTextContent("not set");
  });

  it("shows a persistent restart badge only when requires_restart is non-empty", async () => {
    const rows = PROVIDERS.map((p) =>
      p.source_code === "blockscout"
        ? { ...p, requires_restart: ["api_key" as const] }
        : p,
    );
    fetchMock.mockResolvedValue(json({ data: rows }));
    renderProviders();
    expect(
      await screen.findByTestId("restart-badge-blockscout"),
    ).toBeInTheDocument();
    expect(
      screen.queryByTestId("restart-badge-alchemy"),
    ).not.toBeInTheDocument();
  });

  it("renders enabled_chains distinctly: null=env default, []=none, list=joined", async () => {
    const rows = PROVIDERS.map((p) => {
      if (p.source_code === "alchemy")
        return { ...p, enabled_chains: [] as string[] };
      if (p.source_code === "ankr")
        return { ...p, enabled_chains: ["ethereum", "bsc"] };
      return p; // enabled_chains: null
    });
    fetchMock.mockResolvedValue(json({ data: rows }));
    renderProviders();
    const alchemy = (
      await screen.findByTestId("provider-row-alchemy")
    ).closest("tr")!;
    expect(alchemy).toHaveTextContent("none");
    const ankr = (
      await screen.findByTestId("provider-row-ankr")
    ).closest("tr")!;
    expect(ankr).toHaveTextContent("ethereum, bsc");
    const moralis = (
      await screen.findByTestId("provider-row-moralis")
    ).closest("tr")!;
    expect(moralis).toHaveTextContent("env default");
  });

  it("surfaces an inline Alert with retry on error envelopes", async () => {
    fetchMock
      .mockResolvedValueOnce(
        json({ error: { code: "INTERNAL", message: "boom" } }, 500),
      )
      .mockResolvedValue(json({ data: PROVIDERS }));
    renderProviders();
    expect(
      await screen.findByText(/failed to load providers/i),
    ).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /retry/i }),
    );
    expect(await screen.findByTestId("provider-row-alchemy"))
      .toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("403 shows a lacks-admin-role state without crashing", async () => {
    fetchMock.mockResolvedValue(
      json({ error: { code: "FORBIDDEN", message: "forbidden" } }, 403),
    );
    renderProviders();
    expect(
      await screen.findByText(/lacks admin role/i),
    ).toBeInTheDocument();
  });

  it("row click navigates to /providers/:code", async () => {
    fetchMock.mockResolvedValue(json({ data: PROVIDERS }));
    renderProviders();
    await userEvent.click(
      await screen.findByTestId("provider-row-chainscan"),
    );
    expect(await screen.findByTestId("detail")).toHaveTextContent(
      "detail:chainscan",
    );
  });

  it("network filter tabs show only providers enabled for that chain", async () => {
    const rows: ProviderConfigView[] = [
      makeProvider("ankr", { enabled_chains: ["ethereum", "bsc"] }),
      makeProvider("blockscout", { enabled_chains: ["ethereum"] }),
      makeProvider("nodereal", { enabled_chains: ["bsc"] }),
      makeProvider("moralis", { enabled_chains: null }),
    ];
    fetchMock.mockResolvedValue(json({ data: rows }));
    renderProviders();

    // default: all rows regardless of chains (incl. env-default nulls)
    expect(await screen.findByTestId("provider-row-moralis"))
      .toBeInTheDocument();

    await userEvent.click(await screen.findByTestId("network-filter-ethereum"));
    expect(await screen.findByTestId("provider-row-ankr"))
      .toBeInTheDocument();
    expect(screen.getByTestId("provider-row-blockscout")).toBeInTheDocument();
    expect(screen.queryByTestId("provider-row-nodereal"))
      .not.toBeInTheDocument();
    expect(screen.queryByTestId("provider-row-moralis"))
      .not.toBeInTheDocument();

    await userEvent.click(await screen.findByTestId("network-filter-bsc"));
    expect(await screen.findByTestId("provider-row-nodereal"))
      .toBeInTheDocument();
    expect(screen.getByTestId("provider-row-ankr")).toBeInTheDocument();
    expect(screen.queryByTestId("provider-row-blockscout"))
      .not.toBeInTheDocument();

    await userEvent.click(await screen.findByTestId("network-filter-all"));
    expect(await screen.findByTestId("provider-row-moralis"))
      .toBeInTheDocument();
    expect(screen.getByTestId("provider-row-blockscout")).toBeInTheDocument();
  });
});
