// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type WorkflowRevision } from "../api";
import { SourceTrustReview } from "./SourceTrustReview";

const review = { policy: "same_repository", digest: "sha256:reviewed", allowed: false, reason: "Owner approval required", sources: [{ alias: "app", repository: "team/app", repositoryId: 1, headRepository: "fork/app", headRepositoryId: 2, fork: true, pullRequest: 42, commitSha: "exact-commit", githubAppId: "github" }], credentialScope: ["secret:build-token"], environments: ["preview:target"], checkedAt: "2026-10-01T10:00:00Z" };
const revision = { id: "run", sourceTrust: review } as WorkflowRevision;
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("preview source approval", () => {
  it("requires a refreshed review and explicit acknowledgment before submitting its digest", async () => {
    vi.spyOn(api, "previewSourceTrust").mockResolvedValue(review);
    const approve = vi.spyOn(api, "approvePreviewSourceTrust").mockResolvedValue({});
    const onChanged = vi.fn().mockResolvedValue(undefined);
    render(<SourceTrustReview revision={revision} isOwner onChanged={onChanged} />);
    const button = screen.getByRole("button", { name: "Approve exact sources for one hour" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Refresh source review" }));
    await waitFor(() => expect((screen.getByRole("checkbox") as HTMLInputElement).disabled).toBe(false));
    expect((button as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(button);
    await waitFor(() => expect(approve).toHaveBeenCalledWith("run", review.digest, expect.any(String)));
    expect(screen.getByRole("status").textContent).toContain("Run the preview again");
  });
  it("shows source evidence to viewers without approval controls", () => {
    render(<SourceTrustReview revision={revision} isOwner={false} onChanged={async () => {}} />);
    expect(screen.getByText("exact-commit")).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("button", { name: /Approve exact/ })).toBeNull();
  });
});
