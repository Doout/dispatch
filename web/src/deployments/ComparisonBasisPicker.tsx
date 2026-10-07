import type { DeploymentComparisonBasis } from "../api";

export function ComparisonBasisPicker({ value, onChange }: { value: DeploymentComparisonBasis; onChange: (basis: DeploymentComparisonBasis) => void }) {
 return <label className="comparison-basis">Compare<select aria-label="Comparison details" value={value} onChange={event => onChange(event.target.value as DeploymentComparisonBasis)}>
  <option value="resources">Rendered resources</option>
  <option value="inputs">Saved inputs</option>
 </select></label>;
}
