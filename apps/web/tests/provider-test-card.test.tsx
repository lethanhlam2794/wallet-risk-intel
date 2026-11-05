import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { TestConnectionCard } from "@/features/providers/provider-test-card";
import type { Network } from "@/lib/types";

const fetchMock = vi.fn<typeof fetch>();

// Order mirrors the live API response: bsc first, so a naive networks[0]
// default would wrongly preselect BSC for eth-only providers.
const NETWORKS: Network[] = [
  { code: "bsc", name: "BNB Smart Chain", chain_id: "eip155:56", namespace: "eip155" },
  { code: "ethereum", name: "Ethereum Mainnet", chain_id: "eip155:1", namespace: "eip155" },
];

beforeEach(() => {
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  sessionStorage.clear();
  sessionStorage.setItem("cw.admin.token", "test-token");
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("TestConnectionCard network default", () => {
  it("defaults the select to the provider's first enabled chain", () => {
    render(
      <TestConnectionCard
        code="blockscout"
        networks={NETWORKS}
        defaultNetwork="ethereum"
      />,
    );
    expect(screen.getByTestId("test-network-select")).toHaveTextContent(
      "ethereum",
    );
  });

  it("honours a bsc-only provider over list order", () => {
    render(
      <TestConnectionCard
        code="nodereal"
        networks={NETWORKS}
        defaultNetwork="bsc"
      />,
    );
    expect(screen.getByTestId("test-network-select")).toHaveTextContent("bsc");
  });

  it("falls back to the first network when no default is given", () => {
    render(<TestConnectionCard code="x" networks={NETWORKS} />);
    expect(screen.getByTestId("test-network-select")).toHaveTextContent("bsc");
  });
});
