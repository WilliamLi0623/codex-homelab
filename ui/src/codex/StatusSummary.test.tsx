import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import StatusSummary from "./StatusSummary";
import type { RoutingStatus } from "./types";

describe("routing status summary", () => {
  it("labels a fresh normal route and its next-session model", () => {
    const status: RoutingStatus = { mode: "normal", observed_mode: "normal", observed_at: "2026-09-29T00:00:00Z", generation: 3, fresh: true, published: true, can_start: true, fallback_enabled: false };
    const html = renderToStaticMarkup(<StatusSummary status={status} />);
    expect(html).toContain("Normal route");
    expect(html).toContain("gpt-6-luna");
    expect(html).toContain("Fresh reading");
  });

  it("makes unknown routing explicit and does not suggest a route", () => {
    const status: RoutingStatus = { mode: "unknown", observed_mode: "unknown", observed_at: null, generation: 0, fresh: false, published: false, can_start: false, fallback_enabled: false };
    const html = renderToStaticMarkup(<StatusSummary status={status} />);
    expect(html).toContain("Quota state unknown");
    expect(html).toContain("creating a session will re-check quota");
    expect(html).not.toContain("gpt-6-luna");
  });

  it("labels a persisted route as last known because session creation refreshes quota", () => {
    const status: RoutingStatus = { mode: "normal", observed_mode: "normal", observed_at: "2026-09-29T00:00:00Z", generation: 3, fresh: false, published: true, can_start: true, fallback_enabled: false };
    const html = renderToStaticMarkup(<StatusSummary status={status} />);
    expect(html).toContain("Last published route");
    expect(html).toContain("refreshed before session creation");
  });

  it("shows detected fallback as disabled until the effective subagent route is verified", () => {
    const status: RoutingStatus = { mode: "quota_fallback", observed_mode: "quota_fallback", observed_at: null, generation: 4, fresh: true, published: true, can_start: true, fallback_enabled: false };
    const html = renderToStaticMarkup(<StatusSummary status={status} />);
    expect(html).toContain("Spark route detected · disabled");
    expect(html).toContain("subagent route verification");
  });
});
