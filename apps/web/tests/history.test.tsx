import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthProvider } from "@/auth/auth-context";
import { ProviderDetailPage } from "@/features/providers/provider-detail-page";
import { formatChangeValue } from "@/features/providers/provider-history";
import type { Network, ProviderConfigView } from "@/lib/types";

const fetchMock = vi.fn<typeof fetch>();

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const VIEW: ProviderConfigView = {
  source_code: "chainscan",
  configured: true,
  api_key: { set: true, last4: "4242" },
  base_url: "https://api.chainscan.example",
  enabled_chains: ["ethereum"],
  status: "active",
  catalog_active: true,
  effective_active: true,
  requires_restart: [],
  updated_by: "ops-admin",
  updated_at: "2026-11-04T23:00:00Z",
};

const NETWORKS: Network[] = [
  {
    code: "ethereum",
    name: "Ethereum",
    chain_id: "eip155:1",
    namespace: "eip155",
  },
  { code: "bsc", name: "BNB Smart Chain", chain_id: "eip155:56", namespace: "eip155" },
];

const PAGE1 = {
  items: [
    {
      id: "11111111-1111-1111-1111-111111111111",
      field: "api_key",
      previous_value: { set: false, last4: null },
      new_value: { set: true, last4: "4242" },
      actor_id: "ops-admin",
      actor_type: "api_client",
      changed_at: "2026-11-04T23:00:00Z",
    },
    {
      id: "22222222-2222-2222-2222-222222222222",
      field: "status",
      previous_value: "active",
      new_value: "inactive",
      actor_id: null,
      actor_type: "system",
      changed_at: "2026-11-04T22:00:00Z",
    },
  ],
  next_cursor: "eyJ0IjoiYWJjIn0=",
};

const PAGE2 = {
  items: [
    {
      id: "33333333-3333-3333-3333-333333333333",
      field: "enabled_chains",
      previous_value: null,
      new_value: ["ethereum", "bsc"],
      actor_id: "ops-admin",
      actor_type: "api_client",
      changed_at: "2026-11-04T21:00:00Z",
    },
  ],
  next_cursor: "",
};

function changesCalls() {
  return fetchMock.mock.calls.filter((c) =>
    String(c[0]).includes("/changes"),
  );
}

function stubFetch() {
  fetchMock.mockImplementation(async (input, init) => {
    const url = String(input);
    if (url.endsWith("/api/v1/networks")) return json({ data: NETWORKS });
    if (url.includes("/changes")) {
      const u = new URL(url, "http://localhost");
      return u.searchParams.get("cursor")
        ? json({ data: PAGE2 })
        : json({ data: PAGE1 });
    }
    if ((init?.method ?? "GET").toUpperCase() === "PUT")
      return json({ data: VIEW });
    return json({ data: VIEW });
  });
}

function renderDetail(code = "chainscan") {
  return render(
    <MemoryRouter initialEntries={[`/providers/${code}`]}>
      <AuthProvider>
        <Routes>
          <Route path="/providers/:code" element={<ProviderDetailPage />} />
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

describe("history tab — lazy load and keyset pagination", () => {
  it("does not fetch history until the tab is first activated", async () => {
    stubFetch();
    renderDetail();
    await screen.findByTestId("config-form");
    expect(changesCalls()).toHaveLength(0);
    await userEvent.click(screen.getByTestId("tab-history"));
    await waitFor(() => expect(changesCalls()).toHaveLength(1));
    const url = String(changesCalls()[0]![0]);
    expect(url).toContain("/api/v1/admin/provider-configs/chainscan/changes");
    expect(url).toContain("limit=50");
    expect(url).not.toContain("cursor=");
  });

  it("Load more appends rows and hides the button when next_cursor is ''", async () => {
    stubFetch();
    renderDetail();
    await userEvent.click(await screen.findByTestId("tab-history"));
    const table = await screen.findByTestId("history-table");
    await within(table).findByText("api_key");
    expect(within(table).getAllByRole("row")).toHaveLength(3); // header + 2
    await userEvent.click(screen.getByTestId("load-more"));
    await within(table).findByText("enabled_chains");
    expect(within(table).getAllByRole("row")).toHaveLength(4); // appended
    expect(
      screen.queryByTestId("load-more"),
    ).not.toBeInTheDocument();
    expect(changesCalls()).toHaveLength(2);
  });

  it("sends the opaque cursor verbatim, URL-encoded", async () => {
    stubFetch();
    renderDetail();
    await userEvent.click(await screen.findByTestId("tab-history"));
    await screen.findByTestId("load-more");
    await userEvent.click(screen.getByTestId("load-more"));
    await waitFor(() => expect(changesCalls()).toHaveLength(2));
    const url = String(changesCalls()[1]![0]);
    expect(url).toContain("cursor=eyJ0IjoiYWJjIn0%3D");
  });

  it("masks api_key change values and never renders raw keys", async () => {
    stubFetch();
    renderDetail();
    await userEvent.click(await screen.findByTestId("tab-history"));
    const table = await screen.findByTestId("history-table");
    const apiRow = (await within(table).findByText("api_key")).closest(
      "tr",
    )!;
    expect(apiRow).toHaveTextContent("••••4242");
    expect(apiRow).toHaveTextContent("not set");
    expect(apiRow.textContent).not.toMatch(/[a-z0-9]{16,}/i);
  });

  it("renders actor_id null as — with actor_type suffix", async () => {
    stubFetch();
    renderDetail();
    await userEvent.click(await screen.findByTestId("tab-history"));
    const table = await screen.findByTestId("history-table");
    const statusRow = (await within(table).findByText("status")).closest(
      "tr",
    )!;
    expect(statusRow).toHaveTextContent("—");
    expect(statusRow).toHaveTextContent("system");
  });

  it("renders changed_at as readable UTC", async () => {
    stubFetch();
    renderDetail();
    await userEvent.click(await screen.findByTestId("tab-history"));
    const table = await screen.findByTestId("history-table");
    expect(
      await within(table).findByText(/2026-11-04 23:00:00/),
    ).toBeInTheDocument();
  });

  it("shows the empty state for empty history", async () => {
    fetchMock.mockImplementation(async (input) => {
      const url = String(input);
      if (url.endsWith("/api/v1/networks"))
        return json({ data: NETWORKS });
      if (url.includes("/changes"))
        return json({ data: { items: [], next_cursor: "" } });
      return json({ data: VIEW });
    });
    renderDetail();
    await userEvent.click(await screen.findByTestId("tab-history"));
    expect(await screen.findByTestId("history-empty")).toBeInTheDocument();
    expect(
      screen.queryByTestId("load-more"),
    ).not.toBeInTheDocument();
  });

  it("shows an Alert with retry on error", async () => {
    fetchMock
      .mockImplementation(async (input) => {
        const url = String(input);
        if (url.endsWith("/api/v1/networks"))
          return json({ data: NETWORKS });
        if (url.includes("/changes"))
          return json(
            { error: { code: "INTERNAL", message: "db down" } },
            500,
          );
        return json({ data: VIEW });
      });
    renderDetail();
    await userEvent.click(await screen.findByTestId("tab-history"));
    expect(
      await screen.findByText(/failed to load history/i),
    ).toBeInTheDocument();
    // retry path: re-mock success then click retry
    stubFetch();
    await userEvent.click(
      screen.getByRole("button", { name: /retry/i }),
    );
    const table = await screen.findByTestId("history-table");
    expect(await within(table).findByText("api_key")).toBeInTheDocument();
  });

  it("preserves dirty Config state AND accumulated rows across Config→History→Config→History (forceMount, A2)", async () => {
    stubFetch();
    renderDetail();
    const input = await screen.findByTestId("baseurl-input");
    await userEvent.clear(input);
    await userEvent.type(input, "https://dirty.example.com");
    await userEvent.click(screen.getByTestId("tab-history"));
    const table = await screen.findByTestId("history-table");
    await within(table).findByText("api_key");
    await userEvent.click(screen.getByTestId("load-more"));
    await within(table).findByText("enabled_chains");
    // round-trip to config and back
    await userEvent.click(screen.getByTestId("tab-config"));
    expect(screen.getByTestId("baseurl-input")).toHaveValue(
      "https://dirty.example.com",
    );
    await userEvent.click(screen.getByTestId("tab-history"));
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    // no extra history fetch — accumulated pages persist
    expect(changesCalls()).toHaveLength(2);
  });

  it("hides inactive tabpanels — radix-ui forceMount leaves them visible, so both panels must carry the data-[state=inactive]:hidden class", async () => {
    stubFetch();
    renderDetail();
    await screen.findByTestId("config-form");
    const panels = document.querySelectorAll("[role=tabpanel]");
    expect(panels).toHaveLength(2);
    for (const p of panels) {
      expect(p.className).toContain("data-[state=inactive]:hidden");
    }
    await userEvent.click(screen.getByTestId("tab-history"));
    const states = Array.from(
      document.querySelectorAll("[role=tabpanel]"),
    ).map((p) => p.getAttribute("data-state"));
    expect(states.sort()).toEqual(["active", "inactive"]);
  });
});

describe("detail page navigation", () => {
  it("back-to-providers navigates to /providers", async () => {
    stubFetch();
    render(
      <MemoryRouter initialEntries={["/providers/chainscan"]}>
        <AuthProvider>
          <Routes>
            <Route
              path="/providers/:code"
              element={<ProviderDetailPage />}
            />
            <Route
              path="/providers"
              element={<div data-testid="list-stub">list-stub</div>}
            />
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    );
    await userEvent.click(
      await screen.findByTestId("back-to-providers"),
    );
    expect(await screen.findByTestId("list-stub")).toBeInTheDocument();
  });
});

describe("formatChangeValue", () => {
  it("masks api_key object shapes", () => {
    expect(
      formatChangeValue("api_key", { set: true, last4: "4242" }),
    ).toBe("••••4242");
    expect(formatChangeValue("api_key", { set: true, last4: null })).toBe(
      "••••",
    );
    expect(
      formatChangeValue("api_key", { set: false, last4: null }),
    ).toBe("not set");
  });

  it("normalizes scalar, array, object, and null values", () => {
    expect(formatChangeValue("status", "inactive")).toBe("inactive");
    expect(formatChangeValue("status", null)).toBe("—");
    expect(formatChangeValue("base_url", "")).toBe("(empty)");
    expect(
      formatChangeValue("enabled_chains", ["ethereum", "bsc"]),
    ).toBe("ethereum, bsc");
    expect(formatChangeValue("enabled_chains", [])).toBe("[]");
    expect(formatChangeValue("status", { odd: true })).toBe(
      JSON.stringify({ odd: true }),
    );
  });
});
