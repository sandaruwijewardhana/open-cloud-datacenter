import {
  Body1,
  Button,
  DrawerBody,
  DrawerFooter,
  DrawerHeader,
  DrawerHeaderTitle,
  Field,
  Input,
  MessageBar,
  MessageBarBody,
  OverlayDrawer,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import { Dismiss24Regular } from '@fluentui/react-icons';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useActiveProject } from '../hooks/useActiveProject';
import { ReviewSummary } from './wizard/ReviewSummary';
import { useWizard } from './wizard/CreateWizard';
import type { ValidationIssue } from './wizard/CreateWizard';

const useStyles = makeStyles({
  drawer: { width: '480px' },
  hint: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  body: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  footer: { justifyContent: 'space-between' },
});

export interface RegistryCreated {
  id: string;
  name: string;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onCreated: (reg: RegistryCreated) => void;
}

const STEP_BASICS = 'basics';

export default function RegistryCreateDrawer({ open, onClose, onCreated }: Props) {
  const styles = useStyles();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();

  const wizardResetRef = useRef<() => void>(() => undefined);

  const [name, setName] = useState('');

  const nameValid = /^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$/.test(name);

  const validationIssues: ValidationIssue[] = [];
  if (!nameValid)
    validationIssues.push({ message: 'Registry name is missing or invalid', targetStep: STEP_BASICS });

  const resetFormState = () => setName('');

  const createMutation = useMutation({
    mutationFn: async () => {
      const res = await fetch(
        `/v1/tenants/${tenantId}/projects/${projectId}/registries`,
        {
          method: 'POST',
          credentials: 'include',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name, plan: 'starter' }),
        },
      );
      if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error((err as { error?: string }).error ?? `API error ${res.status}`);
      }
      return res.json() as Promise<{ id: string; name: string }>;
    },
    onSuccess: (reg) => {
      queryClient.invalidateQueries({ queryKey: ['registries', tenantId, projectId] });
      onCreated({ id: reg.id, name: reg.name });
      resetFormState();
      wizardResetRef.current();
      onClose();
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast><ToastTitle>Create failed: {e.message}</ToastTitle></Toast>,
        { intent: 'error' },
      );
    },
  });

  const reviewSummaryContent = (
    <>
      <ReviewSummary rows={[{ key: 'Name', value: name || '—' }, { key: 'Plan', value: 'starter' }]} />
      <MessageBar intent="info">
        <MessageBarBody>
          After the registry becomes <strong>Ready</strong>, retrieve push/pull credentials from the
          registry detail page.
        </MessageBarBody>
      </MessageBar>
    </>
  );

  const wizard = useWizard({
    steps: [
      {
        id: STEP_BASICS,
        title: 'Basics',
        content: (
          <>
            <Body1 className={styles.hint}>
              A container registry gives your project a dedicated Harbor namespace. Provisioning the
              first registry in a tenant takes 3–5 minutes (Harbor Helm deploy); subsequent
              registries in the same tenant are ready in seconds.
            </Body1>
            <Field
              label="Name"
              required
              hint="Lowercase + hyphens, 2-32 chars. Unique within the project."
              validationState={name && !nameValid ? 'error' : 'none'}
              validationMessage={name && !nameValid ? 'Must match [a-z0-9][a-z0-9-]*[a-z0-9].' : undefined}
            >
              <Input
                value={name}
                onChange={(_, d) => setName(d.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
                placeholder="e.g. prod-images"
                autoFocus
              />
            </Field>
          </>
        ),
      },
    ],
    issues: validationIssues,
    reviewSummary: reviewSummaryContent,
    onSubmit: () => createMutation.mutate(),
    onCancel: () => onCloseInternal(),
    submitLabel: 'Create registry',
    submitting: createMutation.isPending,
    submitError: createMutation.isError ? (createMutation.error as Error).message : null,
  });

  useEffect(() => {
    wizardResetRef.current = wizard.reset;
  });

  const onCloseInternal = () => {
    if (createMutation.isPending) return;
    resetFormState();
    wizard.reset();
    onClose();
  };

  return (
    <OverlayDrawer
      open={open}
      onOpenChange={(_, d) => !d.open && onCloseInternal()}
      position="end"
      className={styles.drawer}
    >
      <Toaster toasterId={toasterId} />
      <DrawerHeader>
        <DrawerHeaderTitle
          action={
            <Button
              appearance="subtle"
              icon={<Dismiss24Regular />}
              onClick={onCloseInternal}
              aria-label="Close"
            />
          }
        >
          Create registry
        </DrawerHeaderTitle>
        {wizard.tabList}
      </DrawerHeader>
      <DrawerBody className={styles.body}>
        {wizard.stepContent}
      </DrawerBody>
      <DrawerFooter className={styles.footer}>
        {wizard.footer}
      </DrawerFooter>
    </OverlayDrawer>
  );
}
