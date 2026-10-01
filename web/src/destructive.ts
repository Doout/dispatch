export type DestructiveReview = {
  resourceId: string;
  resourceType: string;
  name: string;
  action: string;
  version: string;
  summary: string;
  resources: string[];
  blockedReason?: string;
};

export type DestructiveConfirmation = {
  resourceId: string;
  action: string;
  expectedVersion: string;
  confirmName: string;
};

type Confirm = (review: DestructiveReview) => Promise<DestructiveConfirmation>;
let handler: Confirm | undefined;

export function registerDestructiveConfirmation(confirm: Confirm) {
  handler = confirm;
  return () => { if (handler === confirm) handler = undefined; };
}

export function requestDestructiveConfirmation(review: DestructiveReview) {
  if (!handler) return Promise.reject(new Error("The confirmation dialog is unavailable. Reload and try again."));
  return handler(review);
}
