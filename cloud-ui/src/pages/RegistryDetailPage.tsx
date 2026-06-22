import {
  Body1,
  Button,
  Card,
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  Spinner,
  Subtitle1,
  Tab,
  TabList,
  Title2,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  shorthands,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  ArrowLeft20Regular,
  Delete20Regular,
  Key20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import StatusPill from '../components/StatusPill';
import SecretRevealBanner, { type Secret } from '../components/SecretRevealBanner';
import RegistryRepositoriesTab from '../components/RegistryRepositoriesTab';
import RegistryConnectInstructions from '../components/RegistryConnectInstructions';
import { useConfirmDialog } from '../components/useConfirmDialog';
import MembersPage from './MembersPage';

interface Registry {
  tenantId: string;
  projectId: string;
  status: string;
  plan?: string;
  registry_url?: string;
  progress?: Record<string, string>;
  message?: string;
  created_at?: string;
}

interface RegistryCredentials {
  robotUsername: string;
  robotPassword: string;
  registryUrl: string;
  loginServer: string;
  harborProject: string;
  caCert?: string;
  quickstart: {
    trustCaLinux?: string;
    trustCaMac?: string;
    trustCaWindows?: string;
    login: string;
    push: string;
    pull: string;
  };
}

const STEP_LABELS: Record<string, string> = {
  '01_helm_install':      'Install Harbor Helm chart',
  '02_harbor_ready':      'Wait for Harbor pods',
  '03_harbor_project':    'Create Harbor project',
  '04_robot_account':     'Create robot account',
  '05_save_credentials':  'Save credentials',
  '06_write_secret':      'Write K8s credentials secret',
  helm_install:           'Install Harbor Helm chart',
  harbor_ready:           'Wait for Harbor pods',
  harbor_project:         'Create Harbor project',
  robot_account:          'Create robot account',
  save_credentials:       'Save credentials',
  write_secret:           'Write K8s credentials secret',
};

const stepLabel = (key: string) => STEP_LABELS[key] ?? key.replace(/_/g, ' ').replace(/^\d+\s*/, '');

const useStyles = makeStyles({
  root: {
    padding: tokens.spacingHorizontalXXL,
    maxWidth: '900px',
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  header: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalS },
  backButton: { alignSelf: 'flex-start' },
  cmdBar: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    paddingTop: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalS,
    ...shorthands.borderTop('1px', 'solid', tokens.colorNeutralStroke2),
    ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
  },
  card: { padding: tokens.spacingHorizontalXXL },
  cardHeader: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalS,
    paddingBottom: tokens.spacingVerticalM,
  },
  grid: {
    display: 'grid',
    gridTemplateColumns: '180px 1fr',
    rowGap: tokens.spacingVerticalM,
    columnGap: tokens.spacingHorizontalL,
  },
  label: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  value: { fontSize: tokens.fontSizeBase300 },
  mono: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    wordBreak: 'break-all',
  },
  loading: {
    padding: tokens.spacingHorizontalXXL,
    display: 'flex',
    justifyContent: 'center',
  },
  credsAction: {
    display: 'flex',
    gap: tokens.spacingHorizontalM,
    alignItems: 'center',
  },
  progressSection: {
    display: 'flex',
    flexDirection: 'column',
    gap: 0,
  },
  progressRow: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: `${tokens.spacingVerticalXS} 0`,
    ...shorthands.borderBottom('1px', 'solid', tokens.colorNeutralStroke2),
  },
  progressLabel: {
    fontSize: tokens.fontSizeBase300,
    color: tokens.colorNeutralForeground1,
  },
});

export default function RegistryDetailPage() {
  const styles = useStyles();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId, projectId, registryId } = useParams<{
    tenantId: string;
    projectId: string;
    registryId: string;
  }>();
  const confirmDialog = useConfirmDialog();

  const [tab, setTab] = useState<'overview' | 'repositories' | 'access'>('overview');
  const [revealedSecrets, setRevealedSecrets] = useState<Secret[] | null>(null);

  const resourceId = registryId ?? projectId;

  const registryQuery = useQuery({
    queryKey: ['registry-detail', tenantId, projectId, resourceId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries/${resourceId}`,
        { credentials: 'include' },
      );
      if (res.status === 404) return null;
      if (!res.ok) throw new Error(`API error ${res.status}`);
      return res.json() as Promise<Registry>;
    },
    refetchInterval: (query) => {
      const data = query.state.data as Registry | null;
      const s = data?.status;
      return s === 'PENDING' || s === 'DELETING' ? 5000 : false;
    },
  });

  const credsMutation = useMutation({
    mutationFn: async () => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries/${resourceId}/credentials`,
        { credentials: 'include' },
      );
      if (res.status === 400) {
        throw new Error('Registry is not ready yet. Wait until status is Ready.');
      }
      if (!res.ok) throw new Error(`API error ${res.status}`);
      return res.json() as Promise<RegistryCredentials>;
    },
    onSuccess: (c) => {
      // Only the sensitive values go in the shown-once reveal banner. The CA cert
      // and per-OS connect steps render in RegistryConnectInstructions below.
      const secrets: Secret[] = [
        { label: 'Robot username', value: c.robotUsername, filename: `${projectId}-robot-username.txt` },
        { label: 'Robot password (token)', value: c.robotPassword, filename: `${projectId}-robot-token.txt` },
        { label: 'Registry URL', value: c.registryUrl },
        { label: 'Harbor project', value: c.harborProject },
      ];
      setRevealedSecrets(secrets);
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>{e.message}</ToastTitle></Toast>,
        { intent: 'warning' },
      );
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries/${resourceId}`,
        { method: 'DELETE', credentials: 'include' },
      );
      if (!res.ok) throw new Error(`API error ${res.status}`);
    },
    onSuccess: () => {
      dispatchToast(
        <Toast><ToastTitle>Delete requested — Harbor teardown in progress.</ToastTitle></Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['registry', tenantId, projectId] });
      navigate(`/tenants/${tenantId}/projects/${projectId}/registries`);
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' },
      );
    },
  });

  const onDelete = async () => {
    const ok = await confirmDialog({
      title: 'Delete container registry?',
      body: 'All Harbor pods, image data, and credentials will be permanently destroyed. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (!ok) return;
    deleteMutation.mutate();
  };

  if (registryQuery.isLoading) {
    return (
      <div className={styles.root}>
        <Toaster toasterId={toasterId} />
        <div className={styles.loading}>
          <Spinner label="Loading registry…" />
        </div>
      </div>
    );
  }

  if (registryQuery.isError || !registryQuery.data) {
    return (
      <div className={styles.root}>
        <Toaster toasterId={toasterId} />
        <Button
          appearance="subtle"
          icon={<ArrowLeft20Regular />}
          onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/registries`)}
          className={styles.backButton}
        >
          Back to registries
        </Button>
        <MessageBar intent={registryQuery.isError ? 'error' : 'warning'}>
          <MessageBarBody>
            <MessageBarTitle>
              {registryQuery.isError ? 'Failed to load registry' : 'Registry not found'}
            </MessageBarTitle>
            {registryQuery.isError
              ? (registryQuery.error instanceof Error ? registryQuery.error.message : 'Unknown error')
              : 'No registry has been provisioned for this project yet.'}
          </MessageBarBody>
        </MessageBar>
      </div>
    );
  }

  const registry = registryQuery.data;
  const isDeleting = registry.status === 'DELETING' || deleteMutation.isPending;
  // dc-api maps the CR's "Ready" phase to the generic resource status "ACTIVE"
  // (same vocabulary as keyvault/database). Treat ACTIVE as ready-to-use.
  const isReady = registry.status === 'ACTIVE';
  const isProvisioning = registry.status === 'PENDING';
  const progressEntries = Object.entries(registry.progress ?? {}).sort(([a], [b]) => a.localeCompare(b));

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <Button
        appearance="subtle"
        icon={<ArrowLeft20Regular />}
        onClick={() => navigate(`/tenants/${tenantId}/projects/${projectId}/registries`)}
        className={styles.backButton}
      >
        Back to registries
      </Button>

      <div className={styles.header}>
        <Title2>Container Registry</Title2>
        <Subtitle1 style={{ color: tokens.colorNeutralForeground3 }}>
          <StatusPill status={registry.status} />
          {registry.message && (
            <span style={{ marginLeft: tokens.spacingHorizontalM, fontSize: tokens.fontSizeBase300 }}>
              {registry.message}
            </span>
          )}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <Button
          appearance="subtle"
          icon={<Delete20Regular />}
          onClick={onDelete}
          disabled={isDeleting || registry.status === 'DELETED'}
        >
          Delete
        </Button>
      </div>

      {isProvisioning && (
        <MessageBar intent="info">
          <MessageBarBody>
            <MessageBarTitle>Provisioning</MessageBarTitle>
            The registry controller is deploying Harbor and setting up your project namespace.
            This typically takes 3–5 minutes. This page refreshes automatically every 5 seconds.
          </MessageBarBody>
        </MessageBar>
      )}

      {registry.status === 'FAILED' && (
        <MessageBar intent="error">
          <MessageBarBody>
            <MessageBarTitle>Provisioning failed</MessageBarTitle>
            {registry.message ?? 'The registry controller reported an error.'} Delete and recreate to retry.
          </MessageBarBody>
        </MessageBar>
      )}

      <TabList selectedValue={tab} onTabSelect={(_, d) => setTab(d.value as typeof tab)}>
        <Tab value="overview">Overview</Tab>
        <Tab value="repositories" disabled={!isReady}>Repositories</Tab>
        <Tab value="access">Access control</Tab>
      </TabList>

      {tab === 'overview' && (
        <>
          <Card className={styles.card}>
            <div className={styles.cardHeader}>
              <Subtitle1>Details</Subtitle1>
            </div>
            <div className={styles.grid}>
              <div className={styles.label}>Project</div>
              <div className={`${styles.value} ${styles.mono}`}>{registry.projectId ?? projectId}</div>

              <div className={styles.label}>Status</div>
              <div><StatusPill status={registry.status} /></div>

              <div className={styles.label}>Plan</div>
              <div className={styles.value}>{registry.plan ?? '—'}</div>

              <div className={styles.label}>Harbor URL</div>
              <div className={styles.value}>
                {registry.registry_url ? (
                  <a
                    href={registry.registry_url}
                    target="_blank"
                    rel="noreferrer"
                    style={{ color: tokens.colorBrandForeground1 }}
                  >
                    {registry.registry_url}
                  </a>
                ) : '—'}
              </div>

              {registry.created_at && (
                <>
                  <div className={styles.label}>Created</div>
                  <div className={styles.value}>
                    {new Date(registry.created_at).toLocaleString()}
                  </div>
                </>
              )}
            </div>
          </Card>

          {isProvisioning && progressEntries.length > 0 && (
            <Card className={styles.card}>
              <div className={styles.cardHeader}>
                <div style={{ display: 'flex', alignItems: 'center', gap: tokens.spacingHorizontalS }}>
                  <Spinner size="tiny" />
                  <Subtitle1>Deployment progress</Subtitle1>
                </div>
              </div>
              <div className={styles.progressSection}>
                {progressEntries.map(([step, status]) => (
                  <div key={step} className={styles.progressRow}>
                    <span className={styles.progressLabel}>{stepLabel(step)}</span>
                    <StatusPill status={status} />
                  </div>
                ))}
              </div>
            </Card>
          )}

          <Card className={styles.card}>
            <div className={styles.cardHeader}>
              <Subtitle1>Project-scoped robot credentials</Subtitle1>
              <Body1 style={{ color: tokens.colorNeutralForeground3 }}>
                Docker-compatible robot account scoped to this project's Harbor namespace.
                Credentials are stored in the cluster and can be retrieved at any time.
              </Body1>
            </div>

            {revealedSecrets && (
              <div style={{ marginBottom: tokens.spacingVerticalL }}>
                <SecretRevealBanner
                  title={`Credentials for project ${registry.projectId ?? projectId}`}
                  description="Store the robot password securely — you can retrieve it again via this page."
                  secrets={revealedSecrets}
                  onDismiss={() => setRevealedSecrets(null)}
                  onCopy={(label) =>
                    dispatchToast(
                      <Toast><ToastTitle>Copied {label}</ToastTitle></Toast>,
                      { intent: 'success' },
                    )
                  }
                />
              </div>
            )}

            {revealedSecrets && credsMutation.data && (
              <div style={{ marginBottom: tokens.spacingVerticalL }}>
                <RegistryConnectInstructions
                  loginServer={credsMutation.data.loginServer}
                  robotUsername={credsMutation.data.robotUsername}
                  harborProject={credsMutation.data.harborProject}
                  caCert={credsMutation.data.caCert}
                />
              </div>
            )}

            <div className={styles.credsAction}>
              <Button
                appearance="primary"
                icon={<Key20Regular />}
                onClick={() => credsMutation.mutate()}
                disabled={!isReady || credsMutation.isPending}
              >
                {credsMutation.isPending ? 'Retrieving…' : 'Get credentials'}
              </Button>
              {!isReady && (
                <Body1 style={{ color: tokens.colorNeutralForeground3 }}>
                  Available once the registry is Ready.
                </Body1>
              )}
            </div>
          </Card>
        </>
      )}

      {tab === 'repositories' && isReady && (
        <RegistryRepositoriesTab
          tenantId={tenantId!}
          projectId={projectId!}
          registryId={resourceId!}
        />
      )}

      {tab === 'access' && (
        <MembersPage
          resourceBase={`/v1/tenants/${tenantId}/projects/${projectId}/registries/${resourceId}`}
          scopeLabel="Container Registry"
        />
      )}
    </div>
  );
}
