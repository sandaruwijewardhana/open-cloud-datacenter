import {
  Body1,
  Button,
  Caption1,
  Subtitle2,
  Tab,
  TabList,
  makeStyles,
  shorthands,
  tokens,
} from '@fluentui/react-components';
import {
  ArrowDownload20Regular,
  Checkmark16Regular,
  Copy16Regular,
} from '@fluentui/react-icons';
import { useState } from 'react';

/**
 * RegistryConnectInstructions renders copy-pasteable, OS-specific steps for
 * authenticating Docker against the tenant's self-signed Harbor registry:
 *   1. download the CA, 2. trust it (per-OS), 3. docker login, 4. push/pull.
 *
 * The CA travels with the credentials (DB-style delivery), so no kubectl is
 * needed on the client machine.
 */
interface Props {
  loginServer: string;
  robotUsername: string;
  harborProject: string;
  caCert?: string;
}

type OS = 'linux' | 'mac' | 'windows';

const useStyles = makeStyles({
  root: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalM },
  caRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
    flexWrap: 'wrap',
  },
  steps: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalL },
  step: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalXS },
  stepHead: { display: 'flex', alignItems: 'center', gap: tokens.spacingHorizontalS },
  badge: {
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    minWidth: '22px',
    height: '22px',
    ...shorthands.borderRadius('999px'),
    backgroundColor: tokens.colorBrandBackground2,
    color: tokens.colorBrandForeground2,
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightSemibold,
  },
  block: {
    position: 'relative',
    backgroundColor: tokens.colorNeutralBackground3,
    ...shorthands.border('1px', 'solid', tokens.colorNeutralStroke2),
    ...shorthands.borderRadius(tokens.borderRadiusMedium),
    ...shorthands.padding(tokens.spacingVerticalS, tokens.spacingHorizontalM),
    paddingRight: '44px',
  },
  pre: {
    margin: 0,
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase200,
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-all',
    color: tokens.colorNeutralForeground1,
  },
  copyBtn: { position: 'absolute', top: '4px', right: '4px' },
  note: { color: tokens.colorNeutralForeground3 },
  subhead: { marginTop: tokens.spacingVerticalS },
});

function CommandBlock({ text }: { text: string }) {
  const styles = useStyles();
  const [copied, setCopied] = useState(false);
  return (
    <div className={styles.block}>
      <pre className={styles.pre}>{text}</pre>
      <Button
        className={styles.copyBtn}
        size="small"
        appearance="subtle"
        icon={copied ? <Checkmark16Regular /> : <Copy16Regular />}
        aria-label="Copy command"
        onClick={() => {
          void navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
      />
    </div>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  const styles = useStyles();
  return (
    <div className={styles.step}>
      <div className={styles.stepHead}>
        <span className={styles.badge}>{n}</span>
        <Body1><strong>{title}</strong></Body1>
      </div>
      {children}
    </div>
  );
}

export default function RegistryConnectInstructions({
  loginServer,
  robotUsername,
  harborProject,
  caCert,
}: Props) {
  const styles = useStyles();
  const [os, setOs] = useState<OS>('linux');

  const host = loginServer;
  const login = `docker login ${host} -u '${robotUsername}'`;
  const push = [
    `docker pull alpine:latest`,
    `docker tag alpine:latest ${host}/${harborProject}/alpine:1`,
    `docker push ${host}/${harborProject}/alpine:1`,
  ].join('\n');

  const downloadCa = () => {
    if (!caCert) return;
    const blob = new Blob([caCert], { type: 'application/x-pem-file' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'ca.crt';
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className={styles.root}>
      <Subtitle2>Connect with Docker</Subtitle2>

      {caCert && (
        <div className={styles.caRow}>
          <Button icon={<ArrowDownload20Regular />} onClick={downloadCa} appearance="primary">
            Download CA certificate (ca.crt)
          </Button>
          <Caption1 className={styles.note}>
            Harbor uses a private TLS cert — save this <code>ca.crt</code>, then follow your OS steps below.
          </Caption1>
        </div>
      )}

      <TabList selectedValue={os} onTabSelect={(_, d) => setOs(d.value as OS)}>
        <Tab value="linux">Linux</Tab>
        <Tab value="mac">macOS</Tab>
        <Tab value="windows">Windows</Tab>
      </TabList>

      <div className={styles.steps}>
        {os === 'linux' && (
          <>
            {caCert && (
              <Step n={1} title="Trust the CA (Docker Engine)">
                <Caption1 className={styles.note}>
                  Add the CA to the system trust store (Harbor's token service is validated against it), then restart Docker. Run from the folder with ca.crt.
                </Caption1>
                <CommandBlock
                  text={`sudo cp ca.crt /usr/local/share/ca-certificates/${host}.crt\nsudo update-ca-certificates\nsudo systemctl restart docker`}
                />
              </Step>
            )}
            <Step n={caCert ? 2 : 1} title="Log in">
              <Caption1 className={styles.note}>Paste the robot token (above) when prompted for the password.</Caption1>
              <CommandBlock text={login} />
            </Step>
            <Step n={caCert ? 3 : 2} title="Push & pull">
              <CommandBlock text={push} />
            </Step>
          </>
        )}

        {os === 'mac' && (
          <>
            {caCert && (
              <Step n={1} title="Trust the CA">
                <Body1 className={styles.subhead}>Colima</Body1>
                <Caption1 className={styles.note}>Docker runs in the Colima VM — add the CA to the VM's system trust store, then restart.</Caption1>
                <CommandBlock
                  text={`colima ssh -- sudo tee /usr/local/share/ca-certificates/${host}.crt < ca.crt > /dev/null\ncolima ssh -- sudo update-ca-certificates\ncolima restart`}
                />
                <Body1 className={styles.subhead}>Docker Desktop</Body1>
                <Caption1 className={styles.note}>Add the CA to the login keychain (no admin needed), then restart Docker Desktop.</Caption1>
                <CommandBlock
                  text={`security add-trusted-cert -r trustAsRoot -k ~/Library/Keychains/login.keychain-db ca.crt`}
                />
              </Step>
            )}
            <Step n={caCert ? 2 : 1} title="Log in">
              <Caption1 className={styles.note}>Paste the robot token (above) when prompted for the password.</Caption1>
              <CommandBlock text={login} />
            </Step>
            <Step n={caCert ? 3 : 2} title="Push & pull">
              <CommandBlock text={push} />
            </Step>
          </>
        )}

        {os === 'windows' && (
          <>
            {caCert && (
              <Step n={1} title="Trust the CA (Docker Desktop)">
                <Caption1 className={styles.note}>PowerShell as Administrator, from the folder with ca.crt. Restart Docker Desktop after.</Caption1>
                <CommandBlock text={`Import-Certificate -FilePath .\\ca.crt -CertStoreLocation Cert:\\LocalMachine\\Root`} />
              </Step>
            )}
            <Step n={caCert ? 2 : 1} title="Log in">
              <Caption1 className={styles.note}>Paste the robot token (above) when prompted for the password.</Caption1>
              <CommandBlock text={login} />
            </Step>
            <Step n={caCert ? 3 : 2} title="Push & pull">
              <CommandBlock text={push} />
            </Step>
          </>
        )}
      </div>
    </div>
  );
}
