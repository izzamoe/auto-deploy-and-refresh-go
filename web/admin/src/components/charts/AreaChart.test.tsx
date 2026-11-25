// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { AreaChart } from "./AreaChart";

afterEach(cleanup);

function labelsOf(days: number): string[] {
  return Array.from({ length: days }, (_, i) => `${i + 1} Nov`);
}

function renderChart(days: number) {
  const labels = labelsOf(days);
  const values = labels.map((_, i) => i % 3);
  return render(
    <AreaChart
      labels={labels}
      series={[
        { key: "ok", label: "Berhasil", values, tone: "success" },
        { key: "bad", label: "Gagal", values: values.map(() => 0), tone: "danger" },
      ]}
      ariaLabel="Grafik"
      tableCaption="Tabel"
      dateHeader="Tanggal"
    />,
  );
}

const axisLabels = (container: HTMLElement) => [...container.querySelectorAll("svg text")].filter((t) => /Nov$/.test(t.textContent ?? ""));

describe("AreaChart x axis (visual review item 2)", () => {
  it.each([7, 30])("always shows the newest date, anchored to the end so it is not clipped (%i days)", (days) => {
    const { container } = renderChart(days);
    const texts = axisLabels(container);
    const last = texts[texts.length - 1];
    expect(last).toHaveTextContent(`${days} Nov`);
    expect(last).toHaveAttribute("text-anchor", "end");
    // every other label is centred (or starts at the axis for the first point)
    for (const t of texts.slice(0, -1)) expect(["middle", "start"]).toContain(t.getAttribute("text-anchor"));
  });

  it("thins the labels so neighbours never overlap (>= 64 px apart at the default width)", () => {
    const { container } = renderChart(30);
    const xs = axisLabels(container).map((t) => Number(t.getAttribute("x")));
    expect(xs.length).toBeGreaterThan(2);
    for (let i = 1; i < xs.length; i++) expect(xs[i] - xs[i - 1]).toBeGreaterThanOrEqual(64);
  });

  it("keeps the axis text at 12 px (text-xs), never the old 11 px", () => {
    const { container } = renderChart(7);
    for (const t of container.querySelectorAll("svg text")) {
      expect(t.getAttribute("class")).toContain("text-xs");
      expect(t.getAttribute("class")).not.toContain("11px");
    }
  });

  it("exposes the same values as a table for screen readers", () => {
    renderChart(7);
    expect(screen.getByTestId("chart-table").querySelectorAll("tbody tr")).toHaveLength(7);
  });
});
