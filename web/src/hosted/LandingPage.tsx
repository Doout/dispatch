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
              Your code. <br />
              Ready to deploy.
            </h1>
            <p className="hosted-hero-description">
              Build images, run parallel deployments, and follow every run from
              one console.
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
            <figcaption>From repository to deployment</figcaption>
            <ol>
              {[
                {
                  icon: GitBranch,
                  title: "Connect your repository",
                  body: "Use your branches and pull requests.",
                },
                {
                  icon: Cube,
                  title: "Build an image",
                  body: "Run jobs on your Docker builders.",
                },
                {
                  icon: RocketLaunch,
                  title: "Deploy to your targets",
                  body: "Promote releases or deploy in parallel.",
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
            <h2 id="features-title">Keep the whole run in view.</h2>
            <p>Configuration, builds and deployments belong together.</p>
          </header>
          <div className="hosted-feature-layout">
            <article className="hosted-feature-primary">
              <GitPullRequest size={28} aria-hidden="true" />
              <h3>Give every change its own deployment.</h3>
              <p>
                Run parallel deployments for pull requests. Check the result,
                follow the logs, and clean up when the work is done.
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
                  <h3>Find the failure while it happens.</h3>
                  <p>
                    Watch build and deployment logs live, then return to any
                    saved run.
                  </p>
                </div>
              </article>
              <article>
                <HardDrives size={24} aria-hidden="true" />
                <div>
                  <h3>Use the infrastructure you have.</h3>
                  <p>
                    Connect deployment targets, Docker builders and event relays
                    to the same workflow.
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
              Start with Docker. Connect your repositories and deployment
              targets, then manage releases through the UI or API.
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
