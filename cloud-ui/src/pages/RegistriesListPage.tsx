import {
  Button,
  Card,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Subtitle1,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Title2,
  Toast,
  ToastTitle,
  Toaster,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowClockwise20Regular,
  BoxMultiple24Regular,
  MoreHorizontal20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useActiveProject } from '../hooks/useActiveProject';
import { useCan } from '../api/useCan';
import StatusPill from '../components/StatusPill';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { useListPageStyles } from '../components/list/useListPageStyles';
import { useConfirmDialog } from '../components/useConfirmDialog';
import { PermissionTooltip } from '../components/PermissionTooltip';
import RegistryCreateDrawer, { type RegistryCreated } from '../components/RegistryCreateDrawer';
import { fmtDate } from '../lib/date';

interface Registry {
  id: string;
  name: string;
  status: string;
  plan?: string;
  registry_url?: string;
  created_at?: string;
  message?: string;
}

export default function RegistriesListPage() {
  const styles = useListPageStyles();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const { can } = useCan(tenantId, ['registry/registries/write'], projectId);
  const canWrite = can('registry/registries/write');
  const navigate = useNavigate();
  const confirmDialog = useConfirmDialog();

  const [createOpen, setCreateOpen] = useState(false);

  const registriesQuery = useQuery({
    queryKey: ['registries', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries`,
        { credentials: 'include' },
      );
      if (!res.ok) throw new Error(`API error ${res.status}`);
      return res.json() as Promise<Registry[]>;
    },
    refetchInterval: (query) => {
      const data = query.state.data as Registry[] | undefined;
      return data?.some((r) => r.status === 'PENDING' || r.status === 'DELETING')
        ? 5000
        : false;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries/${id}`,
        { method: 'DELETE', credentials: 'include' },
      );
      if (!res.ok) throw new Error(`API error ${res.status}`);
    },
    onSuccess: () => {
      dispatchToast(
        <Toast><ToastTitle>Registry deletion started</ToastTitle></Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['registries', tenantId, projectId] });
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' },
      );
    },
  });

  const onDelete = async (reg: Registry) => {
    const ok = await confirmDialog({
      title: `Delete registry "${reg.name}"?`,
      body: 'All Harbor project data, images, and credentials will be permanently destroyed. This cannot be undone.',
      confirmLabel: 'Delete',
      destructive: true,
      typeToConfirm: reg.name,
    });
    if (!ok) return;
    deleteMutation.mutate(reg.id);
  };

  const onCreated = (reg: RegistryCreated) => {
    dispatchToast(
      <Toast>
        <ToastTitle>Registry "{reg.name}" created — provisioning in background…</ToastTitle>
      </Toast>,
      { intent: 'success' },
    );
    navigate(`${reg.id}`);
  };

  const regs = registriesQuery.data ?? [];
  const count = regs.length;

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.header}>
        <Title2>Container Registries</Title2>
        <Subtitle1 className={styles.subtitle}>
          Per-project Harbor namespaces. Each registry is isolated with its own robot account credentials.
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <PermissionTooltip when={!canWrite} reason="You need write access on this project to create a registry">
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            onClick={() => setCreateOpen(true)}
            disabledFocusable={!canWrite}
          >
            Create registry
          </Button>
        </PermissionTooltip>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => registriesQuery.refetch()}
          disabled={registriesQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      {registriesQuery.isLoading && <LoadingState label="Loading registries…" />}

      {registriesQuery.isError && !registriesQuery.isLoading && (
        <ErrorState message="Could not load registries" />
      )}

      {!registriesQuery.isLoading && !registriesQuery.isError && count === 0 && (
        <EmptyState
          icon={<BoxMultiple24Regular />}
          title="No registries in this project yet"
          description="A registry gives your project a dedicated Harbor namespace to push and pull Docker images using project-scoped robot account credentials."
          action={
            <PermissionTooltip when={!canWrite} reason="You need write access on this project to create a registry">
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                onClick={() => setCreateOpen(true)}
                disabledFocusable={!canWrite}
              >
                Create registry
              </Button>
            </PermissionTooltip>
          }
        />
      )}

      {!registriesQuery.isLoading && !registriesQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Container registries">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>Plan</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {regs.map((reg) => (
                <TableRow key={reg.id}>
                  <TableCell>
                    <Link to={`${reg.id}`} className={styles.nameLink}>
                      {reg.name}
                    </Link>
                    <div className={styles.tableMutedCell}>{reg.id}</div>
                  </TableCell>
                  <TableCell><StatusPill status={reg.status} /></TableCell>
                  <TableCell className={styles.tableMutedCell}>{reg.plan ?? '—'}</TableCell>
                  <TableCell className={styles.tableMutedCell}>
                    {reg.created_at ? fmtDate(reg.created_at) : '—'}
                  </TableCell>
                  <TableCell>
                    <Menu>
                      <MenuTrigger disableButtonEnhancement>
                        <Button
                          appearance="subtle"
                          icon={<MoreHorizontal20Regular />}
                          aria-label="Actions"
                        />
                      </MenuTrigger>
                      <MenuPopover>
                        <MenuList>
                          <MenuItem onClick={() => navigate(`${reg.id}`)}>Open</MenuItem>
                          <MenuItem
                            onClick={() => onDelete(reg)}
                            disabled={
                              !canWrite ||
                              deleteMutation.isPending ||
                              reg.status === 'DELETING'
                            }
                          >
                            Delete
                          </MenuItem>
                        </MenuList>
                      </MenuPopover>
                    </Menu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <RegistryCreateDrawer
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={onCreated}
      />
    </div>
  );
}
