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
    <img
      className="brand-icon"
      src="/brand/doodship-icon-32.png"
      srcSet="/brand/doodship-icon-32.png 32w, /brand/doodship-icon-64.png 64w, /brand/doodship-icon-128.png 128w"
      sizes="36px"
      width={32}
      height={32}
      alt=""
      aria-hidden="true"
      decoding="async"
    />
  );
}
