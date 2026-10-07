import type { Overview } from "./api";
import { uiFeatures, type UIFeatureKey } from "./featureFlags";

export function DisabledUIFeature({ overview, feature }: { overview: Overview; feature: UIFeatureKey }) {
  const label = uiFeatures.find(item => item.key === feature)!.label;
  return <section className="section-empty" role="status">
    <h2>{label} is disabled</h2>
    {overview.identity?.systemRole === "owner"
      ? <p>Enable it under <a href="/settings">Settings → Experimental UI</a>.</p>
      : <p>A controller owner can enable this in Settings.</p>}
  </section>;
}
