import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, it } from "vitest";
import { installCommand } from "../src/hosted/InstanceInstall";
const enrollment = {
  enrollmentToken: "one-use-secret",
  platformURL: "https://doodship.test",
  subject: "owner",
  expiresAt: "2099-01-01T00:00:00Z",
};

it("runs the copied curl command with stdin intact and stops on a failed download", () => {
  const scratch = mkdtempSync(join(tmpdir(), "doodship-curl-"));
  try {
    writeFileSync(
      join(scratch, "curl"),
      `#!/bin/sh
if [ "$TEST_FAIL" = 1 ]; then
  printf 'touch "$TEST_OUTPUT/unexpected"\\n'
  exit 22
fi
printf 'cat > "$TEST_OUTPUT/connection"\\n'
`,
      { mode: 0o700 },
    );
    const env = {
      ...process.env,
      PATH: `${scratch}:${process.env.PATH}`,
      TEST_OUTPUT: scratch,
    };
    const secret = `secret-$(touch "${scratch}/unexpected")-\`touch "${scratch}/unexpected"\`-'`;
    const command = installCommand(
      { ...enrollment, enrollmentToken: secret },
      "id",
      true,
      "",
    );
    execFileSync("sh", ["-c", command], { env });
    expect(
      JSON.parse(readFileSync(join(scratch, "connection"), "utf8"))
        .enrollmentToken,
    ).toBe(secret);
    expect(() => readFileSync(join(scratch, "unexpected"))).toThrow();
    const result = spawnSync("sh", ["-c", command], {
      env: { ...env, TEST_FAIL: "1" },
    });
    expect(result.status).toBe(22);
    expect(() => readFileSync(join(scratch, "unexpected"))).toThrow();
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
});

it("bounds downloads and fails on an empty installer response", () => {
  const command = installCommand(enrollment, "id", true, "");
  expect(command).toContain("--connect-timeout 10 --max-time 60 --retry 2");
  expect(command).toContain("--retry-max-time 90 --max-filesize 65536");
  const scratch = mkdtempSync(join(tmpdir(), "doodship-empty-"));
  try {
    writeFileSync(join(scratch, "curl"), "#!/bin/sh\nexit 0\n", {
      mode: 0o700,
    });
    const result = spawnSync("sh", ["-c", command], {
      env: { ...process.env, PATH: `${scratch}:${process.env.PATH}` },
    });
    expect(result.status).toBe(1);
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
});
