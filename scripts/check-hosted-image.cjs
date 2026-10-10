const { execFileSync } = require('node:child_process');

const digestPattern = /^sha256:[a-f0-9]{64}$/;

function checkHostedImage({ image, digest, sha }, docker = args => execFileSync('docker', args, { encoding: 'utf8' })) {
  if (!image || !digestPattern.test(digest) || !/^[a-f0-9]{40}$/.test(sha)) {
    throw new Error('An image, manifest digest and source commit are required.');
  }
  const manifest = JSON.parse(docker(['buildx', 'imagetools', 'inspect', '--raw', `${image}@${digest}`]));
  if (!Array.isArray(manifest.manifests)) {
    throw new Error('The hosted image must contain a manifest for each architecture.');
  }
  const platforms = ['amd64', 'arm64'].map(architecture => {
    const matches = manifest.manifests.filter(entry => entry.platform?.os === 'linux' &&
      entry.platform.architecture === architecture &&
      entry.annotations?.['vnd.docker.reference.type'] !== 'attestation-manifest');
    if (matches.length !== 1 || !digestPattern.test(matches[0].digest)) {
      throw new Error(`Expected one valid Linux ${architecture} image manifest.`);
    }
    return { architecture, digest: matches[0].digest };
  });
  for (const platform of platforms) {
    // Docker's classic image store cannot load two platforms by the same index digest.
    const version = docker(['run', '--rm', '--platform', `linux/${platform.architecture}`,
      '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
      `${image}@${platform.digest}`, 'version']).trimEnd();
    if (version !== `Dispatch platform main-${sha}`) {
      throw new Error(`The Linux ${platform.architecture} image reports an unexpected version.`);
    }
  }
}

module.exports = checkHostedImage;

if (require.main === module) {
  checkHostedImage({ image: process.env.PLATFORM_IMAGE, digest: process.env.IMAGE_DIGEST, sha: process.env.SOURCE_SHA });
}
