import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';
import FeatureHighlights from '@site/src/components/FeatureHighlights';
import Reveal from '@site/src/components/Reveal';

import styles from './index.module.css';

function HomepageHeader() {
  return (
    <header className={clsx('hero hero--primary', styles.heroBanner)}>
      <div className="container">
        <div className={clsx(styles.heroGridSingle, 'text--center')}>
          <Heading as="h1" className="hero__title">
            Kubernetes declares.
            <br />
            Kairon orchestrates.
          </Heading>
          <p className="hero__subtitle">
            Kairon is Zyvor's Apache-2.0 control plane for running virtual
            machines on Kubernetes through FluxVM. A <code>Machine</code>{' '}
            is desired state in the API. A small cluster controller places
            it. A node-local agent turns that into FluxVM — QEMU, Cloud
            Hypervisor, Firecracker, or the FluxVM hypervisor — on real
            KVM. No per-VM wrapper Pod, no libvirt, no guessed hypervisor
            migration endpoints bolted onto the Kubernetes API.
          </p>
          <div className={styles.buttons}>
            <Link
              className="button button--secondary button--lg"
              to="https://github.com/zyvorai/kairon#quick-start">
              Get Started
            </Link>
            <Link
              className="button button--outline button--lg button--secondary"
              to="https://github.com/zyvorai/kairon">
              View on GitHub
            </Link>
          </div>
        </div>
      </div>
    </header>
  );
}

function ProblemStatement() {
  return (
    <section className={styles.problem}>
      <div className="container">
        <Reveal className="row">
          <div className="col col--8 col--offset-2 text--center">
            <Heading as="h2" className={styles.sectionHeading}>
              Why Kairon
            </Heading>
            <p>
              KubeVirt-style stacks run each VM inside a virt-launcher Pod
              wrapping libvirt — a large Go/operator surface with scheduling
              extras bolted onto the Pod scheduler. Kairon takes a
              different, smaller path: a node agent talks directly to{' '}
              <Link to="https://github.com/zyvorai/fluxvm">FluxVM</Link>'s
              REST API, with Kairon's own least-loaded placement deciding
              where a <code>Machine</code> lands.
            </p>
            <p>
              Runtime code is Go standard library only — no client-go, no
              generated deep call stacks, no vendored operator framework.{' '}
              <code>kairon-controller</code> and <code>kairon-node</code>{' '}
              are each small enough to read in an afternoon. And when a
              live migration commit becomes genuinely ambiguous, Kairon
              names that state (<code>NeedsRecovery</code>) and waits for
              an operator's attested decision, rather than risking
              split-brain.
            </p>
          </div>
        </Reveal>
      </div>
    </section>
  );
}

function TrustBand() {
  return (
    <section className={styles.trust}>
      <div className="container">
        <Reveal className={styles.trustGrid}>
          <div>
            <Heading as="h3" className={styles.sectionHeading}>
              Open, and honest about its limits
            </Heading>
            <p>
              Apache-2.0. v0.4.0's own Status section lists real, currently
              open gaps rather than a marketing gloss: cold relocation,
              snapshots, DRA bridging, the secure live control plane, and
              a real FluxVM migration adapter are tested — but real
              two-host live migration hasn't yet been exercised against
              real hardware in this repo's own CI. HA fencing, PVC attach
              maturity, and full admission/quotas are tracked on the
              roadmap, not claimed as done.
            </p>
            <Link to="https://github.com/zyvorai/kairon#status">
              Read the full status section →
            </Link>
          </div>
          <div className={styles.trustBadges}>
            <img
              src="https://github.com/zyvorai/kairon/actions/workflows/ci.yml/badge.svg"
              alt="CI status"
            />
            <img
              src="https://img.shields.io/github/license/zyvorai/kairon"
              alt="Apache 2.0 license"
            />
          </div>
        </Reveal>
      </div>
    </section>
  );
}

function EnterpriseCTA() {
  return (
    <section className={styles.enterprise}>
      <div className="container text--center">
        <Reveal>
          <Heading as="h2" className={styles.sectionHeading}>
            Need production support or SLAs?
          </Heading>
          <p className={styles.enterpriseCopy}>
            Kairon's core is Apache-2.0 and free to run in personal, lab,
            and commercial production use at no charge. Zyvor Enterprise
            adds production support, SLAs, and additional products for
            teams that need them.
          </p>
          <Link
            className="button button--primary button--lg"
            href="mailto:sales@zyvor.dev">
            Contact sales@zyvor.dev
          </Link>
        </Reveal>
      </div>
    </section>
  );
}

export default function Home(): ReactNode {
  return (
    <Layout
      title="Kairon — Kubernetes-native VMs without KubeVirt"
      description="Kairon is Zyvor's Apache-2.0 control plane for running virtual machines on Kubernetes through FluxVM — no virt-launcher Pod, no libvirt.">
      <HomepageHeader />
      <main>
        <ProblemStatement />
        <Reveal>
          <FeatureHighlights />
        </Reveal>
        <TrustBand />
        <EnterpriseCTA />
      </main>
    </Layout>
  );
}
