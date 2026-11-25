// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { Providers } from "@/components/shell/testUtils";
import NotFound from "./not-found";

beforeEach(() => window.localStorage.setItem("ad.locale", "id"));
afterEach(cleanup);

describe("not-found page (Hermes finding on the real binary)", () => {
  it("is centered, balanced and offers a green button back to the dashboard", () => {
    render(
      <Providers>
        <NotFound />
      </Providers>,
    );
    const wrap = screen.getByTestId("not-found-wrap");
    expect(wrap.className).toMatch(/\bgrid\b/);
    expect(wrap.className).toMatch(/place-items-center/);
    expect(wrap.className).toMatch(/min-h-/);
    expect(screen.getByText("Alamat yang Anda buka tidak ada di panel admin.").className).toMatch(/text-balance/);
    const home = screen.getByRole("link", { name: "Kembali ke dasbor" });
    expect(home.className).toMatch(/bg-primary/); // a button, not a plain text link
    expect(home.className).toMatch(/text-primary-foreground/);
  });
});
