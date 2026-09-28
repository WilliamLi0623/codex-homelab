import type { RoutingStatus } from "./types";

function observedLabel(status: RoutingStatus): string {
  if (!status.observed_at) return "No quota observation available";
  if (!status.fresh) return `Last known reading · ${new Date(status.observed_at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}`;
  return `Fresh reading · ${new Date(status.observed_at).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}`;
}

function routeDescription(status: RoutingStatus): string {
  if (status.mode === "normal") {
    if (!status.can_start || !status.published) return "OpenAI · gpt-6-luna · high — publication is not ready";
    return status.fresh
      ? "OpenAI · gpt-6-luna · high"
      : "Last published route · OpenAI · gpt-6-luna · high; refreshed before session creation.";
  }
  if (status.mode === "quota_fallback") {
    return status.fallback_enabled
      ? "Spark · muse-spark-1.3-contributor · xhigh"
      : "Spark route detected · disabled pending subagent route verification";
  }
  return "No route from the current snapshot; creating a session will re-check quota.";
}

export default function StatusSummary({ status }: { status: RoutingStatus }) {
  const label = status.mode === "normal" ? "Normal route" : status.mode === "quota_fallback" ? "Quota exhausted" : "Quota state unknown";
  return (
    <section className="route-summary" aria-label="Quota and next route">
      <div className="route-summary__state">
        <span className={`route-indicator route-indicator--${status.mode}`} aria-hidden="true" />
        <div>
          <h2>{label}</h2>
          <p>{observedLabel(status)}</p>
        </div>
      </div>
      <div className="route-summary__selection">
        <span className="field-label">Next new session</span>
        <p>{routeDescription(status)}</p>
      </div>
      <div className="route-summary__generation">
        <span className="field-label">Route generation</span>
        <strong>{status.generation}</strong>
      </div>
    </section>
  );
}
