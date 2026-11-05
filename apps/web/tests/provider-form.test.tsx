import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { AuthProvider } from "@/auth/auth-context";
import { ProviderDetailPage } from "@/features/providers/provider-detail-page";
import type {
  Network,
  ProviderConfigView,
  ProviderConfigWarning,
} from "@/lib/types";

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
  { code: "ethereum", name: "Ethereum", chain_id: "eip155:1", namespace: "eip155" },
  { code: "bsc", name: "BNB Smart Chain", chain_id: "eip155:56", namespace: "eip155" },
];

function stubFetch(
  view: ProviderConfigView = VIEW,
  putResponse?: { data: ProviderConfigView; warnings?: ProviderConfigWarning[] },
) {
  fetchMock.mockImplementation(async (input, init) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    if (url.endsWith("/api/v1/networks")) return json({ data: NETWORKS });
    if (method === "PUT") return json(putResponse ?? { data: view });
    if (url.includes("/changes"))
      return json({ data: { items: [], next_cursor: "" } });
    return json({ data: view });
  });
}

function putCalls() {
  return fetchMock.mock.calls.filter(
    (c) => (c[1]?.method ?? "GET").toUpperCase() === "PUT",
  );
}

function lastPutBody(): Record<string, unknown> {
  const call = putCalls().at(-1);
  if (!call) throw new Error("no PUT call recorded");
  return JSON.parse(String(call[1]!.body)) as Record<string, unknown>;
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

describe("PATCH discipline — dirty fields only", () => {
  it("editing base_url sends exactly {base_url} and no other keys", async () => {
    stubFetch();
    renderDetail();
    const input = await screen.findByTestId("baseurl-input");
    await userEvent.clear(input);
    await userEvent.type(input, "https://api.new.example");
    await userEvent.click(screen.getByTestId("save-button"));
    await waitFor(() => expect(putCalls()).toHaveLength(1));
    const body = lastPutBody();
    expect(Object.keys(body).sort()).toEqual(["base_url"]);
    expect(body.base_url).toBe("https://api.new.example");
  });

  it("blanking the base_url input omits base_url from the body", async () => {
    stubFetch();
    renderDetail();
    const input = await screen.findByTestId("baseurl-input");
    await userEvent.clear(input);
    // Dirty another field so the patch is non-empty and a PUT can fire.
    await userEvent.click(screen.getByTestId("status-select"));
    await userEvent.click(
      await screen.findByRole("option", { name: /inactive/i }),
    );
    await userEvent.click(screen.getByTestId("save-button"));
    await waitFor(() => expect(putCalls()).toHaveLength(1));
    const body = lastPutBody();
    expect(body).not.toHaveProperty("base_url");
    expect(body).toEqual({ status: "inactive" });
  });

  it("explicit Clear + confirm sends base_url: '' — and the dialog precedes any PUT", async () => {
    stubFetch();
    renderDetail();
    await screen.findByTestId("baseurl-input");
    await userEvent.click(screen.getByTestId("baseurl-clear"));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toBeInTheDocument();
    expect(putCalls()).toHaveLength(0);
    await userEvent.click(
      within(dialog).getByRole("button", { name: /confirm|clear/i }),
    );
    expect(putCalls()).toHaveLength(0);
    await userEvent.click(screen.getByTestId("save-button"));
    await waitFor(() => expect(putCalls()).toHaveLength(1));
    expect(lastPutBody()).toEqual({ base_url: "" });
  });

  it("hides the Clear control when stored base_url is null", async () => {
    stubFetch({ ...VIEW, base_url: null });
    renderDetail();
    await screen.findByTestId("baseurl-input");
    expect(
      screen.queryByTestId("baseurl-clear"),
    ).not.toBeInTheDocument();
  });

  it("status change sends {status:'inactive'} only", async () => {
    stubFetch();
    renderDetail();
    await screen.findByTestId("status-select");
    await userEvent.click(screen.getByTestId("status-select"));
    await userEvent.click(
      await screen.findByRole("option", { name: /inactive/i }),
    );
    await userEvent.click(screen.getByTestId("save-button"));
    await waitFor(() => expect(putCalls()).toHaveLength(1));
    expect(lastPutBody()).toEqual({ status: "inactive" });
  });
});

describe("PATCH discipline — api_key (two-state, write-only)", () => {
  it("is never prefilled and shows masked placeholder", async () => {
    stubFetch();
    renderDetail();
    const input = await screen.findByTestId("apikey-input");
    expect(input).toHaveValue("");
    expect(input).toHaveAttribute("placeholder", "set ••••4242");
    expect(input).toHaveAttribute("type", "password");
    expect(input).toHaveAttribute("autocomplete", "new-password");
  });

  it("renders no Clear control for api_key", async () => {
    stubFetch();
    renderDetail();
    await screen.findByTestId("apikey-input");
    expect(
      screen.queryByTestId("apikey-clear"),
    ).not.toBeInTheDocument();
    // base_url has the only clear affordance
    expect(screen.getByTestId("baseurl-clear")).toBeInTheDocument();
  });

  it("typed value is sent as {api_key}", async () => {
    stubFetch();
    renderDetail();
    const input = await screen.findByTestId("apikey-input");
    await userEvent.type(input, "new-secret-key");
    await userEvent.click(screen.getByTestId("save-button"));
    await waitFor(() => expect(putCalls()).toHaveLength(1));
    expect(lastPutBody()).toEqual({ api_key: "new-secret-key" });
  });

  it("enc: prefixed key is rejected client-side — inline error, zero requests", async () => {
    stubFetch();
    renderDetail();
    const input = await screen.findByTestId("apikey-input");
    await userEvent.type(input, "enc:ciphertext");
    expect(
      await screen.findByTestId("field-error-api_key"),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("save-button"));
    expect(putCalls()).toHaveLength(0);
  });
});

describe("PATCH discipline — enabled_chains", () => {
  it("empty selection requires confirm, then sends enabled_chains: []", async () => {
    stubFetch();
    renderDetail();
    const checkbox = await screen.findByTestId("chain-check-ethereum");
    await userEvent.click(checkbox);
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/every chain|disabl/i);
    await userEvent.click(
      within(dialog).getByRole("button", { name: /confirm|disable/i }),
    );
    expect(putCalls()).toHaveLength(0);
    await userEvent.click(screen.getByTestId("save-button"));
    await waitFor(() => expect(putCalls()).toHaveLength(1));
    expect(lastPutBody()).toEqual({ enabled_chains: [] });
  });

  it("cancelling the empty-chains confirm keeps the selection; nothing is sent", async () => {
    stubFetch();
    renderDetail();
    const checkbox = await screen.findByTestId("chain-check-ethereum");
    await userEvent.click(checkbox);
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: /cancel/i }),
    );
    expect(
      await screen.findByTestId("chain-check-ethereum"),
    ).toBeChecked();
    expect(screen.getByTestId("save-button")).toBeDisabled();
    expect(putCalls()).toHaveLength(0);
  });

  it("toggling to a different selection sends the whole array", async () => {
    stubFetch();
    renderDetail();
    await userEvent.click(
      await screen.findByTestId("chain-check-bsc"),
    );
    await userEvent.click(screen.getByTestId("save-button"));
    await waitFor(() => expect(putCalls()).toHaveLength(1));
    const body = lastPutBody();
    expect(Object.keys(body).sort()).toEqual(["enabled_chains"]);
    expect(body.enabled_chains).toEqual(
      expect.arrayContaining(["ethereum", "bsc"]),
    );
    expect(body.enabled_chains).toHaveLength(2);
  });

  it("shows env-default caption when stored enabled_chains is null", async () => {
    stubFetch({ ...VIEW, enabled_chains: null });
    renderDetail();
    const fieldset = await screen.findByTestId("chains-fieldset");
    expect(fieldset).toHaveTextContent(/env default/i);
    expect(fieldset).not.toHaveTextContent(/disabled on every chain\.$/);
  });
});

describe("PATCH discipline — elision and save gating (A3)", () => {
  it("edit-then-revert to stored value elides the field; fully-elided patch disables Save and sends zero PUTs", async () => {
    stubFetch();
    renderDetail();
    const input = await screen.findByTestId("baseurl-input");
    await userEvent.type(input, "-dirty-suffix");
    expect(screen.getByTestId("save-button")).toBeEnabled();
    await userEvent.clear(input);
    await userEvent.type(input, "https://api.chainscan.example");
    expect(screen.getByTestId("save-button")).toBeDisabled();
    await userEvent.click(screen.getByTestId("save-button"));
    expect(putCalls()).toHaveLength(0);
  });

  it("reordered-but-equal enabled_chains is omitted", async () => {
    stubFetch({ ...VIEW, enabled_chains: ["ethereum", "bsc"] });
    renderDetail();
    const eth = await screen.findByTestId("chain-check-ethereum");
    await userEvent.click(eth); // [bsc]
    await userEvent.click(eth); // [bsc, eth] — set-equal to stored
    expect(screen.getByTestId("save-button")).toBeDisabled();
    expect(putCalls()).toHaveLength(0);
  });

  it("Save is disabled while a PUT is in-flight — double-click sends one request", async () => {
    let resolvePut: (r: Response) => void = () => {};
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.endsWith("/api/v1/networks"))
        return json({ data: NETWORKS });
      if (method === "PUT")
        return new Promise<Response>((r) => {
          resolvePut = r;
        });
      return json({ data: VIEW });
    });
    renderDetail();
    const input = await screen.findByTestId("baseurl-input");
    await userEvent.clear(input);
    await userEvent.type(input, "https://api.other.example");
    const save = screen.getByTestId("save-button");
    await userEvent.click(save);
    expect(save).toBeDisabled();
    await userEvent.click(save);
    resolvePut(json({ data: VIEW }));
    await waitFor(() => expect(putCalls()).toHaveLength(1));
  });
});

describe("post-write state — warnings and badges", () => {
  it("renders warnings[] with correct severities, unknown codes as generic warning, and keeps the restart badge", async () => {
    const updated: ProviderConfigView = {
      ...VIEW,
      status: "inactive",
      requires_restart: ["api_key"],
    };
    stubFetch(VIEW, {
      data: updated,
      warnings: [
        {
          code: "last_active_member",
          network: "ethereum",
          message: "failover chain left empty",
        },
        { code: "no_enabled_chains", message: "disabled everywhere" },
        { code: "requires_restart", message: "stored differs in-effect" },
        { code: "future_code_x", message: "unknown warning" },
      ],
    });
    renderDetail();
    await userEvent.click(await screen.findByTestId("status-select"));
    await userEvent.click(
      await screen.findByRole("option", { name: /inactive/i }),
    );
    await userEvent.click(screen.getByTestId("save-button"));
    const region = await screen.findByTestId("warnings-region");
    expect(
      within(region).getByTestId("warning-last_active_member"),
    ).toHaveAttribute("data-severity", "destructive");
    expect(
      within(region).getByTestId("warning-no_enabled_chains"),
    ).toHaveAttribute("data-severity", "destructive");
    expect(
      within(region).getByTestId("warning-requires_restart"),
    ).toHaveAttribute("data-severity", "warning");
    expect(
      within(region).getByTestId("warning-future_code_x"),
    ).toHaveAttribute("data-severity", "warning");
    expect(region).toHaveTextContent("failover chain left empty");
    expect(region).toHaveTextContent("unknown warning");
    expect(
      await screen.findByTestId("restart-badge"),
    ).toBeInTheDocument();
  });
});

describe("error mapping", () => {
  it("400 echoes error.message + allowed_hosts next to base_url", async () => {
    fetchMock.mockImplementation(async (input, init) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      if (url.endsWith("/api/v1/networks"))
        return json({ data: NETWORKS });
      if (method === "PUT")
        return json(
          {
            error: {
              code: "BAD_REQUEST",
              message: 'host "evil.example.com" not in allowlist',
              allowed_hosts: ["api.etherscan.io", "api.bscscan.com"],
            },
          },
          400,
        );
      return json({ data: VIEW });
    });
    renderDetail();
    const input = await screen.findByTestId("baseurl-input");
    await userEvent.clear(input);
    await userEvent.type(input, "https://evil.example.com");
    await userEvent.click(screen.getByTestId("save-button"));
    const err = await screen.findByTestId("field-error-base_url");
    expect(err).toHaveTextContent("not in allowlist");
    expect(err).toHaveTextContent("api.etherscan.io");
    expect(err).toHaveTextContent("api.bscscan.com");
  });

  it("404 on load renders a not-found state", async () => {
    fetchMock.mockImplementation(async (input) => {
      const url = String(input);
      if (url.endsWith("/api/v1/networks"))
        return json({ data: NETWORKS });
      return json(
        { error: { code: "NOT_FOUND", message: "no such provider" } },
        404,
      );
    });
    renderDetail();
    expect(
      await screen.findByText(/not found|does not exist/i),
    ).toBeInTheDocument();
  });
});

describe("tabs state persistence (A2 forceMount)", () => {
  it("dirty Config form state survives a Config→History→Config round-trip", async () => {
    stubFetch();
    renderDetail();
    const input = await screen.findByTestId("baseurl-input");
    await userEvent.clear(input);
    await userEvent.type(input, "https://dirty.example.com");
    await userEvent.click(screen.getByTestId("tab-history"));
    await userEvent.click(screen.getByTestId("tab-config"));
    expect(screen.getByTestId("baseurl-input")).toHaveValue(
      "https://dirty.example.com",
    );
  });
});
