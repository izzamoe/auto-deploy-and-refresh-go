// @vitest-environment jsdom
// The `asChild` slot of <Button> keeps the merge rules of the Radix Slot it replaces (002 T072).
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { Button } from "./button";
import { Slot } from "./slot";

afterEach(cleanup);

describe("Slot / Button asChild", () => {
  it("renders the child element with the button classes joined to the child's", () => {
    render(
      <Button asChild variant="outline" size="sm">
        <a href="/apps/" className="own">
          Apps
        </a>
      </Button>,
    );
    const a = screen.getByRole("link", { name: "Apps" });
    expect(a.tagName).toBe("A");
    expect(a).toHaveClass("own", "border", "h-8");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("runs both handlers (child first), lets the child's props win, merges styles and both refs", () => {
    const order: string[] = [];
    const slotRef = createRef<HTMLElement>();
    const childRef = createRef<HTMLAnchorElement>();
    render(
      <Slot ref={slotRef} title="slot" style={{ color: "red", margin: 1 }} onClick={() => order.push("slot")}>
        <a ref={childRef} href="/x" title="child" style={{ margin: 2 }} onClick={() => order.push("child")}>
          X
        </a>
      </Slot>,
    );
    const a = screen.getByRole("link", { name: "X" });
    fireEvent.click(a);
    expect(order).toEqual(["child", "slot"]);
    expect(a).toHaveAttribute("title", "child");
    expect(a.style.color).toBe("red");
    expect(a.style.margin).toBe("2px");
    expect(slotRef.current).toBe(a);
    expect(childRef.current).toBe(a);
  });

  it("renders nothing for a non-element child", () => {
    const { container } = render(<Slot>{"text" as unknown as never}</Slot>);
    expect(container).toBeEmptyDOMElement();
  });
});
