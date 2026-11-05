import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Button, EmptyState } from "@/components";

describe("EmptyState", () => {
  it("renders title and description", () => {
    render(<EmptyState title="No items" description="Create one to begin" />);
    expect(screen.getByText("No items")).toBeInTheDocument();
    expect(screen.getByText("Create one to begin")).toBeInTheDocument();
  });

  it("renders an action slot", async () => {
    const user = userEvent.setup();
    const onCreate = vi.fn();
    render(
      <EmptyState
        title="No items"
        action={<Button onClick={onCreate}>Create item</Button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Create item" }));
    expect(onCreate).toHaveBeenCalledTimes(1);
  });

  it("renders an icon slot", () => {
    render(
      <EmptyState
        title="Empty"
        icon={<svg data-testid="empty-icon" />}
      />,
    );
    expect(screen.getByTestId("empty-icon")).toBeInTheDocument();
  });
});
