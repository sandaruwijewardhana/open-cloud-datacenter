import {
  Badge,
  Body1,
  Button,
  Card,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  ArrowClockwise20Regular,
  BoxMultiple24Regular,
  ChevronDown20Regular,
  ChevronRight20Regular,
  Copy20Regular,
  Delete20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { EmptyState, ErrorState, LoadingState } from './list/PageStates';
import { useListPageStyles } from './list/useListPageStyles';
import { useConfirmDialog } from './useConfirmDialog';
import { fmtDate } from '../lib/date';

interface Repository {
  name: string;
  fullName: string;
  artifactCount: number;
  pullCount: number;
  updatedAt: string;
  pullCommand: string;
}

interface Artifact {
  digest: string;
  shortDigest: string;
  tags: string[];
  size: number;
  sizeHuman: string;
  pushedAt: string;
  mediaType: string;
  pullCommands: string[];
}

interface Props {
  tenantId: string;
  projectId: string;
  registryId: string;
}

const useStyles = makeStyles({
  tabCard: { padding: tokens.spacingHorizontalL, marginTop: tokens.spacingVerticalM },
  cmdBar: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    marginBottom: tokens.spacingVerticalM,
  },
  expandBtn: {
    minWidth: 0,
    padding: `0 ${tokens.spacingHorizontalXS}`,
  },
  monoCell: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  artifactRow: {
    background: tokens.colorNeutralBackground2,
  },
  artifactCell: {
    paddingLeft: tokens.spacingHorizontalXXL,
  },
  tagBadge: {
    marginRight: tokens.spacingHorizontalXS,
  },
  pullCmd: {
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    maxWidth: '380px',
    display: 'block',
  },
});

function ArtifactRows({
  tenantId,
  projectId,
  registryId,
  repoName,
}: {
  tenantId: string;
  projectId: string;
  registryId: string;
  repoName: string;
}) {
  const styles = useStyles();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const queryClient = useQueryClient();
  const confirmDialog = useConfirmDialog();

  const artifactsQuery = useQuery<{ artifacts: Artifact[] }>({
    queryKey: ['registry-artifacts', tenantId, projectId, registryId, repoName],
    queryFn: async () => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries/${registryId}/repositories/${encodeURIComponent(repoName)}/artifacts`,
        { credentials: 'include' },
      );
      if (!res.ok) throw new Error(`API error ${res.status}`);
      return res.json();
    },
  });

  const deleteArtifactMutation = useMutation({
    mutationFn: async (reference: string) => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries/${registryId}/repositories/${encodeURIComponent(repoName)}/artifacts/${encodeURIComponent(reference)}`,
        { method: 'DELETE', credentials: 'include' },
      );
      if (!res.ok) throw new Error(`API error ${res.status}`);
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Artifact deleted.</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({
        queryKey: ['registry-artifacts', tenantId, projectId, registryId, repoName],
      });
      queryClient.invalidateQueries({ queryKey: ['registry-repositories', tenantId, projectId, registryId] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onDeleteArtifact = async (reference: string) => {
    const ok = await confirmDialog({
      title: 'Delete artifact?',
      body: `This will permanently remove the artifact ${reference} and all its tags. This cannot be undone.`,
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (ok) deleteArtifactMutation.mutate(reference);
  };

  if (artifactsQuery.isLoading) {
    return (
      <TableRow>
        <TableCell colSpan={5} className={styles.artifactCell}>
          <Spinner size="tiny" label="Loading artifacts…" />
        </TableCell>
      </TableRow>
    );
  }

  const artifacts = artifactsQuery.data?.artifacts ?? [];

  if (artifacts.length === 0) {
    return (
      <TableRow>
        <TableCell colSpan={5} className={styles.artifactCell}>
          <Body1 style={{ color: tokens.colorNeutralForeground3 }}>No artifacts in this repository.</Body1>
        </TableCell>
      </TableRow>
    );
  }

  return (
    <>
      <Toaster toasterId={toasterId} />
      {artifacts.map((a) => (
        <TableRow key={a.digest} className={styles.artifactRow}>
          <TableCell className={styles.artifactCell}>
            <span className={styles.monoCell}>{a.shortDigest}</span>
          </TableCell>
          <TableCell>
            {a.tags.length > 0
              ? a.tags.map((t) => (
                  <Badge key={t} appearance="tint" className={styles.tagBadge}>{t}</Badge>
                ))
              : <span style={{ color: tokens.colorNeutralForeground3 }}>untagged</span>}
          </TableCell>
          <TableCell className={styles.monoCell}>{a.sizeHuman}</TableCell>
          <TableCell>
            {a.pullCommands[0] ? (
              <span className={styles.pullCmd} title={a.pullCommands[0]}>
                {a.pullCommands[0]}
              </span>
            ) : '—'}
          </TableCell>
          <TableCell>
            <Button
              appearance="subtle"
              size="small"
              icon={<Copy20Regular />}
              disabled={!a.pullCommands[0]}
              onClick={() => a.pullCommands[0] && navigator.clipboard.writeText(a.pullCommands[0])}
              title="Copy pull command"
            />
            <Button
              appearance="subtle"
              size="small"
              icon={<Delete20Regular />}
              onClick={() => onDeleteArtifact(a.digest)}
              title="Delete artifact"
            />
          </TableCell>
        </TableRow>
      ))}
    </>
  );
}

export default function RegistryRepositoriesTab({ tenantId, projectId, registryId }: Props) {
  const listStyles = useListPageStyles();
  const styles = useStyles();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const queryClient = useQueryClient();
  const confirmDialog = useConfirmDialog();
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  const reposQuery = useQuery<{ repositories: Repository[] }>({
    queryKey: ['registry-repositories', tenantId, projectId, registryId],
    queryFn: async () => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries/${registryId}/repositories`,
        { credentials: 'include' },
      );
      if (!res.ok) throw new Error(`API error ${res.status}`);
      return res.json();
    },
  });

  const deleteRepoMutation = useMutation({
    mutationFn: async (repoName: string) => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries/${registryId}/repositories/${encodeURIComponent(repoName)}`,
        { method: 'DELETE', credentials: 'include' },
      );
      if (!res.ok) throw new Error(`API error ${res.status}`);
    },
    onSuccess: (_, repoName) => {
      dispatchToast(<Toast><ToastTitle>Repository deleted.</ToastTitle></Toast>, { intent: 'success' });
      setExpanded((prev) => { const n = new Set(prev); n.delete(repoName); return n; });
      queryClient.invalidateQueries({ queryKey: ['registry-repositories', tenantId, projectId, registryId] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onDeleteRepo = async (repoName: string) => {
    const ok = await confirmDialog({
      title: 'Delete repository?',
      body: `All images and tags in "${repoName}" will be permanently deleted. This cannot be undone.`,
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (ok) deleteRepoMutation.mutate(repoName);
  };

  const toggleExpand = (name: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };

  const repos = reposQuery.data?.repositories ?? [];

  return (
    <Card className={styles.tabCard}>
      <Toaster toasterId={toasterId} />

      <div className={styles.cmdBar}>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => reposQuery.refetch()}
          disabled={reposQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {reposQuery.isLoading && <LoadingState label="Loading repositories…" />}
      {reposQuery.isError && <ErrorState message="Could not load repositories" />}

      {!reposQuery.isLoading && !reposQuery.isError && repos.length === 0 && (
        <EmptyState
          icon={<BoxMultiple24Regular />}
          title="No repositories"
          description="Push a Docker image to this registry to see it here. Use the credentials from the Overview tab to authenticate."
        />
      )}

      {!reposQuery.isLoading && !reposQuery.isError && repos.length > 0 && (
        <Table size="small" aria-label="Repositories">
          <TableHeader>
            <TableRow>
              <TableHeaderCell style={{ width: 32 }}></TableHeaderCell>
              <TableHeaderCell>Repository</TableHeaderCell>
              <TableHeaderCell>Artifacts</TableHeaderCell>
              <TableHeaderCell>Pulls</TableHeaderCell>
              <TableHeaderCell>Last updated</TableHeaderCell>
              <TableHeaderCell style={{ width: 80 }}></TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {repos.map((repo) => {
              const isOpen = expanded.has(repo.name);
              return (
                <>
                  <TableRow key={repo.name}>
                    <TableCell>
                      <Button
                        appearance="subtle"
                        size="small"
                        className={styles.expandBtn}
                        icon={isOpen ? <ChevronDown20Regular /> : <ChevronRight20Regular />}
                        onClick={() => toggleExpand(repo.name)}
                        aria-label={isOpen ? 'Collapse' : 'Expand'}
                      />
                    </TableCell>
                    <TableCell>
                      <span style={{ fontWeight: tokens.fontWeightSemibold }}>{repo.name}</span>
                    </TableCell>
                    <TableCell>{repo.artifactCount}</TableCell>
                    <TableCell>{repo.pullCount}</TableCell>
                    <TableCell className={listStyles.tableMutedCell}>
                      {fmtDate(repo.updatedAt)}
                    </TableCell>
                    <TableCell>
                      <Button
                        appearance="subtle"
                        size="small"
                        icon={<Copy20Regular />}
                        onClick={() => navigator.clipboard.writeText(repo.pullCommand)}
                        title="Copy pull command"
                      />
                      <Button
                        appearance="subtle"
                        size="small"
                        icon={<Delete20Regular />}
                        onClick={() => onDeleteRepo(repo.name)}
                        title="Delete repository"
                      />
                    </TableCell>
                  </TableRow>
                  {isOpen && (
                    <>
                      <TableRow key={`${repo.name}-header`} className={styles.artifactRow}>
                        <TableCell />
                        <TableHeaderCell className={styles.artifactCell}>Digest</TableHeaderCell>
                        <TableHeaderCell>Tags</TableHeaderCell>
                        <TableHeaderCell>Size</TableHeaderCell>
                        <TableHeaderCell>Pull command</TableHeaderCell>
                        <TableHeaderCell></TableHeaderCell>
                      </TableRow>
                      <ArtifactRows
                        tenantId={tenantId}
                        projectId={projectId}
                        registryId={registryId}
                        repoName={repo.name}
                      />
                    </>
                  )}
                </>
              );
            })}
          </TableBody>
        </Table>
      )}
    </Card>
  );
}
