import { describe, expect, it } from "vitest";
import { render } from "svelte/server";
import LoadingSpinner from "./LoadingSpinner.svelte";

describe("LoadingSpinner", () => {
  it("renders pixel-aligned blocks at size 24 without SVG scaling or SMIL tags", () => {
    const { body } = render(LoadingSpinner, {
      props: {
        label: "Sit back while it load...",
        size: 24,
      },
    });

    expect(body).toContain('role="status"');
    expect(body).toContain("Sit back while it load...");
    expect(body).toContain("width:10px; height:10px");
    expect(body).toContain("translate(0px, 12px)");
    expect(body).not.toContain("<animate");
    expect(body).not.toContain("<svg");
  });

  it("renders integer-aligned inline span at size 14 without role=status for button nesting", () => {
    const { body } = render(LoadingSpinner, {
      props: {
        inline: true,
        size: 14,
        stepMs: 80,
        label: "Syncing",
      },
    });

    expect(body).toContain("<span");
    expect(body).toContain("Syncing");
    expect(body).toContain("width:6px; height:6px");
    expect(body).toContain("translate(0px, 8px)");
    expect(body).not.toContain("<div");
    expect(body).not.toContain('role="status"');
  });
});
