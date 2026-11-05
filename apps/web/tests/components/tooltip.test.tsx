import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Button, Tooltip } from "@/components";

describe("Tooltip", () => {
  it("hides content until interaction", () => {
    render(
      <Tooltip content="Helpful hint" delayDuration={0}>
        <Button>Hover me</Button>
      </Tooltip>,
    );
    expect(screen.queryByText("Helpful hint")).not.toBeInTheDocument();
  });

  it("shows content on focus without any external provider", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip content="Helpful hint" delayDuration={0}>
        <Button>Focus me</Button>
      </Tooltip>,
    );
    await user.tab();
    expect(screen.getByRole("button", { name: "Focus me" })).toHaveFocus();
    expect(await screen.findAllByText("Helpful hint")).not.toHaveLength(0);
  });

  it("shows content on hover", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip content="Hover hint" delayDuration={0}>
        <Button>Hover me</Button>
      </Tooltip>,
    );
    await user.hover(screen.getByRole("button", { name: "Hover me" }));
    expect(await screen.findAllByText("Hover hint")).not.toHaveLength(0);
  });

  it("dismisses on Escape", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip content="Dismiss me" delayDuration={0}>
        <Button>Focus me</Button>
      </Tooltip>,
    );
    await user.tab();
    expect(await screen.findAllByText("Dismiss me")).not.toHaveLength(0);
    await user.keyboard("{Escape}");
    expect(screen.queryByText("Dismiss me")).not.toBeInTheDocument();
  });
});
