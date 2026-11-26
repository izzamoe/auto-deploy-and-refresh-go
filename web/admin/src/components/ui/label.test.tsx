// @vitest-environment jsdom
// The native label keeps the behaviour of the Radix label it replaces (002 T072).
import { cleanup, createEvent, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Label } from "./label";

afterEach(cleanup);

describe("Label", () => {
  it("labels its control and keeps the label classes", () => {
    render(
      <>
        <Label htmlFor="f-name" className="own">
          Nama
        </Label>
        <input id="f-name" />
      </>,
    );
    const input = screen.getByLabelText("Nama");
    expect(input).toHaveAttribute("id", "f-name");
    const label = screen.getByText("Nama");
    expect(label.tagName).toBe("LABEL");
    expect(label).toHaveClass("own", "text-sm", "font-medium");
  });

  it("a double click on the label does not select its text; a single click is untouched", () => {
    render(<Label htmlFor="x">Teks</Label>);
    const label = screen.getByText("Teks");
    const double = createEvent.mouseDown(label, { detail: 2 });
    fireEvent(label, double);
    expect(double.defaultPrevented).toBe(true);
    const single = createEvent.mouseDown(label, { detail: 1 });
    fireEvent(label, single);
    expect(single.defaultPrevented).toBe(false);
  });
});
