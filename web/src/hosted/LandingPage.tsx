import {
  ArrowRight,
  ArrowUpRight,
  Cube,
  GitBranch,
  GitPullRequest,
  HardDrives,
  RocketLaunch,
  TerminalWindow,
} from "@phosphor-icons/react";
import { HostedBrand } from "./HostedChrome";

const repository = "https://github.com/Doout/dispatch";
export const signInAddress = "/?view=login";

export function LandingPage() {
  return (
    <div className="hosted-public">
      <a className="skip-link" href="#landing-content">
        Skip to content
      </a>
      <header className="hosted-public-header">
        <HostedBrand />
        <nav aria-label="Public navigation">
          <a href="#features">Features</a>
          <a href="#self-host">Self-hosting</a>
          <a className="quiet-button" href={signInAddress}>
            Sign in <ArrowRight size={15} aria-hidden="true" />
          </a>
        </nav>
      </header>
      <main id="landing-content" tabIndex={-1}>
        <section className="hosted-hero" aria-labelledby="landing-title">
          <div className="hosted-hero-copy">
            <p className="hosted-eyebrow">Deployment control</p>
            <h1 id="landing-title">
              Deploy from Git. <br />
              Follow each run.
            </h1>
            <p className="hosted-hero-description">
              Build Docker images and deploy to Docker, Kubernetes or OpenShift.
              Inspect job output and deployment logs as they arrive.
            </p>
            <div className="hosted-hero-actions">
              <a className="primary-button" href={signInAddress}>
                Open console <ArrowRight size={17} aria-hidden="true" />
              </a>
              <a className="hosted-text-link" href={repository}>
                View on GitHub <ArrowUpRight size={16} aria-hidden="true" />
              </a>
            </div>
          </div>
          <figure
            className="hosted-workflow"
            aria-label="A deployment workflow"
          >
            <figcaption>How a deployment runs</figcaption>
            <ol>
              {[
                {
                  icon: GitBranch,
                  title: "Connect your repository",
                  body: "Select a branch or pull request revision.",
                },
                {
                  icon: Cube,
                  title: "Build an image",
                  body: "Run jobs on your Docker builders.",
                },
                {
                  icon: RocketLaunch,
                  title: "Deploy to your targets",
                  body: "Deploy the built image to a target.",
                },
                {
                  icon: TerminalWindow,
                  title: "Follow the run",
                  body: "See live logs and deployment history.",
                },
              ].map(({ icon: Icon, title, body }) => (
                <li key={title}>
                  <span className="hosted-workflow-icon">
                    <Icon size={22} aria-hidden="true" />
                  </span>
                  <div>
                    <strong>{title}</strong>
                    <p>{body}</p>
                  </div>
                </li>
              ))}
            </ol>
          </figure>
        </section>
        <section
          className="hosted-features"
          id="features"
          aria-labelledby="features-title"
        >
          <header>
            <h2 id="features-title">Builds, deployments and logs</h2>
            <p>
              Each run saves its source commits, configuration and job outputs.
            </p>
          </header>
          <div className="hosted-feature-layout">
            <article className="hosted-feature-primary">
              <GitPullRequest size={28} aria-hidden="true" />
              <h3>Parallel deployments for pull requests</h3>
              <p>
                Deploy a pull request without replacing the main application.
                Link changes across repositories and remove the deployment when
                all linked pull requests close.
              </p>
              <div
                className="hosted-feature-flow"
                aria-label="Pull request deployment lifecycle"
              >
                <span>Pull request</span>
                <ArrowRight size={16} aria-hidden="true" />
                <span>Deployment</span>
                <ArrowRight size={16} aria-hidden="true" />
                <span>Cleanup</span>
              </div>
            </article>
            <div className="hosted-feature-secondary">
              <article>
                <TerminalWindow size={24} aria-hidden="true" />
                <div>
                  <h3>Live logs and run history</h3>
                  <p>
                    Read build and deployment output while a run is active. Open
                    a saved run to inspect its logs and failure details.
                  </p>
                </div>
              </article>
              <article>
                <HardDrives size={24} aria-hidden="true" />
                <div>
                  <h3>Deployment targets and Docker builders</h3>
                  <p>
                    Register targets for deployments and builders for image
                    jobs. Event relays receive webhooks for private controllers.
                  </p>
                </div>
              </article>
            </div>
          </div>
        </section>
        <section
          className="hosted-self-host"
          id="self-host"
          aria-labelledby="self-host-title"
        >
          <div>
            <h2 id="self-host-title">Run Dispatch on your own server.</h2>
            <p>
              Install the controller with Docker. Connect a repository and a
              deployment target, then run deployments through the UI, CLI or
              API.
            </p>
          </div>
          <a
            className="quiet-button"
            href={`${repository}#install-and-upgrade`}
          >
            Installation guide <ArrowUpRight size={16} aria-hidden="true" />
          </a>
        </section>
      </main>
      <footer className="hosted-public-footer">
        <HostedBrand />
        <p>Deployments, builds and live logs.</p>
        <a href={repository}>
          Source code <ArrowUpRight size={14} aria-hidden="true" />
        </a>
      </footer>
    </div>
  );
}
