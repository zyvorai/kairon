import type {ReactNode} from 'react';
import Link from '@docusaurus/Link';
import Heading from '@theme/Heading';
import styles from './styles.module.css';

type FeatureItem = {
  title: string;
  description: ReactNode;
  to: string;
};

const FeatureList: FeatureItem[] = [
  {
    title: 'Machine CRD, full lifecycle',
    description:
      'CPU, memory, image, network, power, and volumes as one CRD; PVC-backed boot disks resolve through a Bound PersistentVolumeClaim instead of a hand-placed image file.',
    to: '/docs/architecture',
  },
  {
    title: 'Real placement, not a stub',
    description:
      'Ready, capable-labeled nodes; least-loaded with deterministic tie-break; required hard affinity/anti-affinity co-location and separation constraints against other Machines.',
    to: '/docs/architecture',
  },
  {
    title: 'Secure live migration handshake',
    description:
      'TLS 1.3 mTLS prepare → transfer → commit, no user-supplied tcp: URIs, rollback on transfer failure, and NeedsRecovery when a commit is genuinely ambiguous instead of risking split-brain.',
    to: '/docs/migration-adapter',
  },
  {
    title: 'Network Fabric',
    description:
      'Rich spec.network, MachineNetworkPolicy, NetworkSecurityGroup, and Service Fabric VIP membership through to the FluxVM eBPF edge.',
    to: '/docs/network-fabric',
  },
  {
    title: 'CPU/memory hotplug',
    description:
      "Grow a running Machine's resources without a reboot, via FluxVM's real QMP device_add/object-add — not just cgroup throttling.",
    to: '/docs/getting-started',
  },
  {
    title: 'Go standard library only',
    description:
      'No client-go, no generated deep call stacks, no vendored operator framework — kairon-controller and kairon-node are each small enough to read in an afternoon.',
    to: 'https://github.com/zyvorai/kairon/blob/main/go.mod',
  },
];

function Feature({title, description, to}: FeatureItem) {
  return (
    <div className="col col--4">
      <Link to={to} className={styles.card}>
        <Heading as="h3">{title}</Heading>
        <p>{description}</p>
      </Link>
    </div>
  );
}

export default function FeatureHighlights(): ReactNode {
  return (
    <section className={styles.features}>
      <div className="container">
        <div className="row">
          {FeatureList.map((props, idx) => (
            <Feature key={idx} {...props} />
          ))}
        </div>
      </div>
    </section>
  );
}
