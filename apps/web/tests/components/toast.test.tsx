import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Button, Toaster, toast } from "@/components";

function Trigger({
  label,
  onTrigger,
}: {
  label: string;
  onTrigger: () => void;
}) {
  return <Button onClick={onTrigger}>{label}</Button>;
}

describe("toast", () => {
  it("shows a success toast with its content after a user action", async () => {
    const user = userEvent.setup();
    render(
      <>
        <Toaster />
        <Trigger label="Save" onTrigger={() => toast.success("Saved")} />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();
  });

  it("shows error, info and warning toasts", async () => {
    const user = userEvent.setup();
    render(
      <>
        <Toaster />
        <Trigger label="Err" onTrigger={() => toast.error("Failed hard")} />
        <Trigger label="Inf" onTrigger={() => toast.info("FYI only")} />
        <Trigger label="Warn" onTrigger={() => toast.warning("Careful now")} />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Err" }));
    expect(await screen.findByText("Failed hard")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Inf" }));
    expect(await screen.findByText("FYI only")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Warn" }));
    expect(await screen.findByText("Careful now")).toBeInTheDocument();
  });

  it("supports a loading toast that updates to success", async () => {
    const user = userEvent.setup();
    let id: string | number = "";
    render(
      <>
        <Toaster />
        <Trigger
          label="Start"
          onTrigger={() => {
            id = toast.loading("Saving…");
          }}
        />
        <Trigger
          label="Resolve"
          onTrigger={() => toast.success("Done saving", { id })}
        />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Start" }));
    expect(await screen.findByText("Saving…")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Resolve" }));
    expect(await screen.findByText("Done saving")).toBeInTheDocument();
  });

  it("supports a loading toast that updates to error", async () => {
    const user = userEvent.setup();
    let id: string | number = "";
    render(
      <>
        <Toaster />
        <Trigger
          label="Start"
          onTrigger={() => {
            id = toast.loading("Saving…");
          }}
        />
        <Trigger
          label="Reject"
          onTrigger={() => toast.error("Save failed", { id })}
        />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Start" }));
    expect(await screen.findByText("Saving…")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByText("Save failed")).toBeInTheDocument();
  });

  it("renders description and action affordances", async () => {
    const user = userEvent.setup();
    render(
      <>
        <Toaster />
        <Trigger
          label="Detail"
          onTrigger={() =>
            toast.success("With extras", {
              description: "Extra context",
              action: { label: "Undo", onClick: () => {} },
            })
          }
        />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Detail" }));
    expect(await screen.findByText("With extras")).toBeInTheDocument();
    expect(await screen.findByText("Extra context")).toBeInTheDocument();
    expect(
      await screen.findByRole("button", { name: "Undo" }),
    ).toBeInTheDocument();
  });
});
