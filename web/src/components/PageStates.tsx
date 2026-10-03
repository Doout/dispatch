import { ArrowLeft, Plus } from "@phosphor-icons/react";
import { View } from "../routes";
import { PageHeader } from "../PageHeader";

export function ResourceSummary({
  items,
}: {
  items: Array<{ label: string; value: number }>;
}) {
  return (
    <dl className="resource-summary" aria-label="Resource totals">
      {items.map((item) => (
        <div key={item.label}>
          <dd>{item.value}</dd>
          <dt>{item.label}</dt>
        </div>
      ))}
    </dl>
  );
}

export function EmptyState({
  title,
  body,
  action,
}: {
  title: string;
  body?: string;
  action?: { label: string; onClick: () => void };
}) {
  return (
    <section className="empty-state">
      <Mark />
      <div>
        <h2>{title}</h2>
        {body && <p>{body}</p>}
      </div>
      {action && (
        <button className="quiet-button" onClick={action.onClick}>
          <Plus size={15} weight="bold" />
          {action.label}
        </button>
      )}
    </section>
  );
}

export function UnavailablePage({
  view,
  title,
  body,
  onBack,
}: {
  view: View;
  title: string;
  body: string;
  onBack?: () => void;
}) {
  return (
    <div className="page-layout">
      <PageHeader
        view={view}
        title={title}
        action={
          onBack
            ? {
                label: `Back to ${view}`,
                onClick: onBack,
                icon: <ArrowLeft size={16} />,
                tone: "quiet",
              }
            : undefined
        }
      />
      <EmptyState title={title} body={body} />
    </div>
  );
}

export function PageLoading() {
  return (
    <div className="page-layout">
      <div className="page-loading">
        <span />
        <span />
        <span />
      </div>
    </div>
  );
}

export function Mark() {
  return (
    <svg viewBox="0 0 36 36" aria-hidden="true">
      <path
        d="M5 8.5 18 2l13 6.5v18L18 34 5 26.5Z"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
      />
      <path
        d="m5 8.5 13 7 13-7M18 15.5V34"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
      />
      <path d="m10 11 8-4 8 4-8 4Z" fill="currentColor" />
    </svg>
  );
}
